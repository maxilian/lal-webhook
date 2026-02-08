## LAL-WEBHOOK DEMO

Repo ini bertujuan sebagai demo dari implementasi webhook di lalserver. 

Dilengkapi dengan simple token based quota management.

### Requirements
1) lalserver
2) redis server

### Usage

1) Untuk running webhook gunakan command:
```
go run ./cmd/main.go
```

2) Untuk menjalankan simulasi streaming secara headless gunakan command:
```
go run ./simulation/main.go
```

