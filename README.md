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

lalu ubah setting pada lalserver untuk mengaktifkan http notify/webhook ketika ada event video start stop
```
"http_notify": {
      "enable": true,
      "update_interval_sec": 5,
      "on_sub_start": "http://127.0.0.1:5000/on_sub_start",
      "on_sub_stop": "http://127.0.0.1:5000/on_sub_stop"
    },
```   


2) Untuk menjalankan simulasi streaming secara headless gunakan command:
```
go run ./simulation/main.go
```

3) Untuk melihat list dari quota gunakan akses halaman http://localhost:5000/quotas

4) Untuk manual kick menggunakan curl:
```
curl -X POST http://127.0.0.1:8083/api/ctrl/kick_session \
-H "Content-Type: application/json" \
-d '{"stream_name":"testing", "session_id":"ID dari stream"}'
```


