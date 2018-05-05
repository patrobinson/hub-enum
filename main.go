package main

import (
	"context"
	"encoding/base64"
	"flag"
	"regexp"
	"strconv"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/go-redis/redis"
	"github.com/google/go-github/github"
	"github.com/shurcooL/githubql"
	"golang.org/x/oauth2"
)

/*
	query {
		nodes(ids: $nodeIds) {
			... on User {
				login
				publicKeys {
					edges {
						node {
							key
						}
					}
				}
			}
		}
	}
*/
type userQuery struct {
	Nodes []userResults `graphql:"nodes(ids: $nodeIds)"`
}

type userResults struct {
	User user `graphql:"... on User"`
}

type user struct {
	Login      githubql.String
	PublicKeys struct {
		Edges []publicKey
	} `graphql:"publicKeys(first: 100)"`
}

type publicKey struct {
	Node struct {
		Key githubql.String
	}
}

func main() {
	log.SetLevel(log.DebugLevel)
	var redisConnString = flag.String("redis-server", "localhost:6379", "The hostname and port of the redis instance")
	var githubAuthToken = flag.String("github-oauth-token", "", "The github token to authenticate with")
	flag.Parse()
	auth := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: *githubAuthToken},
	)
	githubV4Client := githubql.NewClient(oauth2.NewClient(context.Background(), auth))
	githubV3Client := github.NewClient(oauth2.NewClient(context.Background(), auth))
	redisClient := redis.NewClient(&redis.Options{
		Addr:     *redisConnString,
		Password: "",
		DB:       0,
	})

	workerChan := make(chan []int64)
	var wg sync.WaitGroup
	wg.Add(1)
	go enumerateGithubUsers(githubV3Client, &workerChan, &wg)
	wg.Add(1)
	go getUserKeys(githubV4Client, redisClient, &workerChan, &wg)
	wg.Wait()
}

func enumerateGithubUsers(client *github.Client, workerChan *chan []int64, wg *sync.WaitGroup) {
	opts := &github.UserListOptions{
		ListOptions: github.ListOptions{
			PerPage: 100,
		},
	}
	var retries int
	for {
		users, _, err := client.Users.ListAll(context.Background(), opts)
		if err != nil {
			if _, ok := err.(*github.RateLimitError); !ok {
				log.Debugln(err)
			} else {
				log.Errorln(err)
			}
			time.Sleep(time.Duration(2^retries*100) * time.Millisecond)
			retries++
			continue
		}
		var payload []int64
		for _, user := range users {
			payload = append(payload, *user.ID)
		}
		log.Debugf("Processing %d users", len(payload))
		*workerChan <- payload
		if len(users) == 0 {
			log.Infoln("Finished processing users")
			break
		}
		opts.Since = *users[len(users)-1].ID
		retries = 0
	}
	close(*workerChan)
	wg.Done()
}

func getUserKeys(githubClient *githubql.Client, redisClient *redis.Client, workerChan *chan []int64, wg *sync.WaitGroup) {
	for userIds := range *workerChan {
		var query userQuery
		var nodeIds []string
		for _, userID := range userIds {
			nodeIds = append(nodeIds, base64.StdEncoding.EncodeToString([]byte("4:User"+strconv.FormatInt(userID, 10))))
		}
		variables := map[string]interface{}{
			"nodeIds": nodeIds,
		}
		queryWithRetries(githubClient, &query, variables)
		for _, userInfo := range query.Nodes {
			for _, keyInfo := range userInfo.User.PublicKeys.Edges {
				redisClient.RPush(string(keyInfo.Node.Key), string(userInfo.User.Login))
			}
		}
	}
	wg.Done()
}

func queryWithRetries(githubClient *githubql.Client, query *userQuery, variables map[string]interface{}) {
	var retries int
	resolutionErrorRegex := regexp.MustCompile("^Could not resolve to a node with the global id of.*")
	for {
		err := githubClient.Query(context.Background(), query, variables)
		if err == nil || resolutionErrorRegex.MatchString(err.Error()) {
			break
		}
		log.Debugln(err)
		time.Sleep(time.Duration(2^retries*100) * time.Millisecond)
		retries++
	}
}
