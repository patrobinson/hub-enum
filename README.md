# Usage

Setup a redis server locally then run:

```
go get -u github.com/patrobinson/hub-enum
hub-enum -github-oauth-token <Github API Token>
```

To find which users are using a public key use redis-cli to run:

```
lrange <public-key> 0 -1
```
