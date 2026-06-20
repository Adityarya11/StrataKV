# HTTP API Reference

StrataKV's optional HTTP server (`cmd/stratakv/main.go`) exposes the engine over a minimal REST interface using Go's standard `net/http` — no external framework.

Base URL: `http://localhost:8080`

All request and response bodies are JSON.

---

## `PUT /put`

Insert or update a key.

**Request body**

```json
{
  "key": "user:101",
  "value": "aditya"
}
```

**Response — 200 OK**

```json
{
  "status": "success"
}
```

**Response — 400 Bad Request**
Returned if `key` is empty or the body fails to decode.

---

## `GET /get?key=<key>`

Retrieve a value by key.

**Response — 200 OK** (key found)

```json
{
  "key": "user:101",
  "value": "aditya",
  "found": true
}
```

**Response — 404 Not Found** (key absent or deleted)

```json
{
  "key": "user:101",
  "found": false
}
```

---

## `DELETE /delete?key=<key>`

Marks a key as deleted via a tombstone. The key is not physically removed until the next compaction.

**Response — 200 OK**

```json
{
  "status": "deleted"
}
```

---

## `POST /compact`

Triggers a synchronous compaction cycle: merges all segment files, drops stale key versions, purges tombstones whose deletes are now physically applied. Blocks all reads and writes for the duration — see [BENCHMARKS.md](BENCHMARKS.md) for typical compaction duration under load.

**Response — 200 OK**

```json
{
  "status": "compaction triggered successfully"
}
```

---

## Error Responses

All error responses return a non-2xx status with a plain-text body via `http.Error`, e.g.:

```
key query parameter is required to request
```

| Status | Meaning                                                         |
| ------ | --------------------------------------------------------------- |
| 400    | Missing or invalid `key` parameter / malformed JSON body        |
| 405    | Wrong HTTP method for the endpoint                              |
| 500    | Internal engine error (WAL write failure, disk I/O error, etc.) |

---

## Using the Go API Directly

If you don't need the HTTP layer, import `engine` directly — see the [README quickstart](README.md#quickstart) and the [real-world integration example](README.md#real-world-usage-compiler-execution-cache).
