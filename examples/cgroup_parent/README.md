Note: if you hit an error like:

```
sockguard_1  | 2018/08/22 20:22:11 listen unix /var/run/docker/sockguard.sock: bind: address already in use
```

starting this, ensure you do a `docker-compose down -v` before `docker-compose up`. The Docker volume retains the old socket on shutdown sometimes.
