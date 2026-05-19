# API Route Demos (Minimal)

Base URL: http://localhost:8080

All request bodies are JSON.

---

## PUT /put

Request body:

```json
{
  "key": "user:101",
  "value": "aditya"
}
```

Response body:

```json
{
  "status": "success"
}
```

---

## GET /get?key=user:101

Request body: none

Response body:

```json
{
  "key": "user:101",
  "value": "aditya",
  "found": true
}
```

---

## DELETE /delete?key=user:101

Request body: none

Response body:

```json
{
  "status": "deleted"
}
```

---

## POST /compact

Request body: none

Response body:

```json
{
  "status": "compaction triggered successfully"
}
```
