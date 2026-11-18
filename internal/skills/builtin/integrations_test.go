package builtin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- sql_query over workspace files ---

func TestSqlQueryOverFiles(t *testing.T) {
	dir := t.TempDir()
	must := func(name, body string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("data/Orders 2025.csv", "\ufeffid,customer_id,amount,note\n1,10,99.5,first\n2,10,0.5,\n3,11,20,\"a, b\"\n")
	must("data/customers.jsonl", `{"id":10,"name":"Acme","tags":["x"],"vip":true}`+"\n\n"+`{"id":11,"name":"Globex"}`+"\n")
	must("data/semi.csv", "a;b\n1;2\n")
	must("data/bad.jsonl", "[1,2]\n")
	exec := SqlQueryContext(SqlConfig{Workspace: WorkspaceConfig{Dir: dir}})
	run := func(args map[string]any) map[string]any {
		t.Helper()
		res, err := exec(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		return res.(map[string]any)
	}

	res := run(map[string]any{"files": []any{"data/Orders 2025.csv", "data/customers.jsonl"}, "action": "tables"})
	tables, _ := res["tables"].([]loadedTable)
	if len(tables) != 2 || tables[0].Table != "orders_2025" || tables[1].Table != "customers" || tables[0].Rows != 3 {
		t.Fatalf("tables %+v", res)
	}
	if strings.Join(tables[0].Columns, ",") != "id INTEGER,customer_id INTEGER,amount REAL,note TEXT" {
		t.Fatalf("columns %v", tables[0].Columns)
	}

	res = run(map[string]any{
		"files": []any{"data/Orders 2025.csv", "data/customers.jsonl"},
		"query": "SELECT c.name, SUM(o.amount) AS total, COUNT(o.note) AS notes FROM orders_2025 o JOIN customers c ON c.id = o.customer_id GROUP BY c.name ORDER BY total DESC",
	})
	rows, _ := res["rows"].([][]any)
	if len(rows) != 2 || rows[0][0] != "Acme" || rows[0][1] != 100.0 || rows[0][2] != int64(1) || rows[1][1] != 20.0 {
		t.Fatalf("join %+v", res)
	}

	// A single file can be given as database; JSON arrays/bools are stored as text.
	res = run(map[string]any{"database": "data/customers.jsonl", "query": "SELECT tags, vip FROM customers WHERE id = 10"})
	if rows, _ := res["rows"].([][]any); len(rows) != 1 || rows[0][0] != `["x"]` || rows[0][1] != int64(1) {
		t.Fatalf("jsonl %+v", res)
	}
	res = run(map[string]any{"files": []any{"data/semi.csv"}, "query": "SELECT a + b FROM semi"})
	if rows, _ := res["rows"].([][]any); len(rows) != 1 || rows[0][0] != int64(3) {
		t.Fatalf("semicolon csv %+v", res)
	}

	for _, bad := range []map[string]any{
		{"files": []any{"data/Orders 2025.csv"}, "query": "DELETE FROM orders_2025"},
		{"files": []any{"data/Orders 2025.csv"}, "query": "SELECT 1; DROP TABLE orders_2025"},
		{"files": []any{"../etc/passwd.csv"}, "query": "SELECT 1"},
		{"files": []any{"data/notes.txt"}, "query": "SELECT 1"},
		{"files": []any{"data/bad.jsonl"}, "query": "SELECT 1"},
		{"files": []any{"data/Orders 2025.csv"}, "query": "SELECT nope FROM orders_2025"},
	} {
		if res := run(bad); res["error"] == nil {
			t.Errorf("%v: expected an error, got %+v", bad, res)
		}
	}
	// The wrong-column error shows the schema so the model can fix the query.
	if res := run(map[string]any{"files": []any{"data/Orders 2025.csv"}, "query": "SELECT nope FROM orders_2025"}); res["tables"] == nil {
		t.Fatal("schema missing from the error")
	}
}

func TestTableName(t *testing.T) {
	taken := map[string]bool{}
	for in, want := range map[string]string{"a/Sales-2025.csv": "sales_2025", "2025.csv": "t_2025", "x/!!.tsv": "t_"} {
		if got := tableName(in, taken); got != want {
			t.Errorf("%s: %s want %s", in, got, want)
		}
	}
	if got := tableName("b/sales_2025.jsonl", taken); got != "sales_2025_2" {
		t.Errorf("duplicate: %s", got)
	}
}

// --- prometheus_query ---

func fakePrometheus(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "u" || p != "pw" {
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		ok := func(data string) { _, _ = io.WriteString(w, `{"status":"success","data":`+data+`}`) }
		switch r.URL.Path {
		case "/prom/api/v1/query":
			if r.URL.Query().Get("query") == "bad(" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"status":"error","errorType":"bad_data","error":"parse error"}`)
				return
			}
			ok(`{"resultType":"vector","result":[
				{"metric":{"__name__":"up","instance":"a:9100","job":"node"},"value":[1700000000,"1"]},
				{"metric":{"__name__":"up","instance":"b:9100","job":"node"},"value":[1700000000,"0"]},
				{"metric":{"instance":"c"},"value":[1700000000,"NaN"]}]}`)
		case "/prom/api/v1/query_range":
			q := r.URL.Query()
			if q.Get("step") == "" || q.Get("start") == "" {
				t.Errorf("range params %v", q)
			}
			var vals []string
			for i := 0; i < 300; i++ {
				vals = append(vals, `[`+jsonNum(1700000000+i*60)+`,"`+jsonNum(i%10)+`"]`)
			}
			ok(`{"resultType":"matrix","result":[{"metric":{"instance":"a"},"values":[` + strings.Join(vals, ",") + `]},
				{"metric":{"instance":"b"},"values":[[1700000000,"50"]]}]}`)
		case "/prom/api/v1/alerts":
			ok(`{"alerts":[{"labels":{"alertname":"DiskFull","severity":"critical","instance":"a"},"annotations":{"summary":"disk 95%"},"state":"firing","activeAt":"2025-01-01T00:00:00Z"},
				{"labels":{"alertname":"Slow"},"state":"pending"}]}`)
		case "/prom/api/v1/targets":
			ok(`{"activeTargets":[{"labels":{"job":"node","instance":"a"},"health":"up"},
				{"labels":{"job":"node","instance":"b"},"health":"down","lastError":"connection refused","scrapeUrl":"http://b/metrics"}]}`)
		case "/prom/api/v1/label/__name__/values":
			ok(`["node_disk_io","node_cpu","up"]`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func jsonNum(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestPrometheusQuery(t *testing.T) {
	srv := fakePrometheus(t)
	defer srv.Close()
	u := strings.Replace(srv.URL, "http://", "http://u:pw@", 1) + "/prom/"
	named, order := ParseNamedURLs([]string{"main=" + u})
	exec := PrometheusQueryContext(PrometheusConfig{Named: named, Order: order}, 5)
	run := func(args map[string]any) map[string]any {
		t.Helper()
		res, err := exec(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		return res.(map[string]any)
	}

	res := run(map[string]any{"query": "up"})
	series, _ := res["series"].([]map[string]any)
	if len(series) != 3 || series[0]["value"] != 1.0 || series[0]["metric"] != `up{instance="a:9100", job="node"}` || series[2]["value"] != "NaN" {
		t.Fatalf("instant %+v", res)
	}
	if res := run(map[string]any{"query": "bad("}); !strings.Contains(res["error"].(string), "parse error") {
		t.Fatalf("error %+v", res)
	}

	res = run(map[string]any{"action": "range", "query": "x", "range": "5h", "chart": true})
	list, _ := res["series"].([]map[string]any)
	if len(list) != 2 || list[0]["metric"] != `{instance="b"}` || list[1]["max"] != 9.0 || list[1]["min"] != 0.0 {
		t.Fatalf("range %+v", res)
	}
	if pts, _ := list[1]["points"].([][2]any); len(pts) != promMaxPoints {
		t.Fatalf("downsampled to %d points", len(pts))
	}
	if b64, _ := res["image_base64"].(string); len(b64) < 1000 {
		t.Fatal("no chart image")
	}
	if res := run(map[string]any{"action": "range", "query": "x", "range": "forever"}); res["error"] == nil {
		t.Fatal("bad range accepted")
	}

	res = run(map[string]any{"action": "alerts"})
	if res["firing_count"] != 1 || res["pending_count"] != 1 {
		t.Fatalf("alerts %+v", res)
	}
	res = run(map[string]any{"action": "targets"})
	if res["down_count"] != 1 || res["targets"] != 2 {
		t.Fatalf("targets %+v", res)
	}
	res = run(map[string]any{"action": "metrics", "match": "node"})
	if res["count"] != 2 {
		t.Fatalf("metrics %+v", res)
	}
	if res := run(map[string]any{"server": "nope", "query": "up"}); res["error"] == nil {
		t.Fatal("unknown server accepted")
	}
	none := PrometheusQueryContext(PrometheusConfig{}, 5)
	if r, _ := none(context.Background(), map[string]any{"query": "up"}); !strings.Contains(r.(map[string]any)["error"].(string), "SKILL_PROMETHEUS") {
		t.Fatalf("unconfigured: %v", r)
	}
}

func TestParsePromDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"90s": 90 * time.Second, "15m": 15 * time.Minute, "2d": 48 * time.Hour, "1.5h": 90 * time.Minute} {
		if got, err := parsePromDuration(in); err != nil || got != want {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	if _, err := parsePromDuration("5 hours"); err == nil {
		t.Error("accepted 5 hours")
	}
}

// --- openapi ---

const petSpec = `
openapi: 3.0.0
info: {title: Pets, version: "1.2"}
servers: [{url: "{base}/v1", variables: {base: {default: "BASE"}}}]
paths:
  /pets:
    get:
      operationId: listPets
      summary: List all pets
      tags: [pets]
      parameters:
        - {name: limit, in: query, schema: {type: integer}}
        - {name: X-Trace, in: header, schema: {type: string}}
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: array, items: {$ref: "#/components/schemas/Pet"}}}}}
    post:
      operationId: createPet
      summary: Create a pet
      requestBody:
        required: true
        content: {application/json: {schema: {$ref: "#/components/schemas/Pet"}}}
      responses: {"201": {description: created}}
  /pets/{petId}:
    parameters:
      - {$ref: "#/components/parameters/PetId"}
    get:
      operationId: showPetById
      summary: Info for a specific pet
      responses: {"200": {description: ok}}
    delete:
      summary: Delete a pet
      responses: {"204": {description: gone}}
components:
  parameters:
    PetId: {name: petId, in: path, required: true, description: The id, schema: {type: string}}
  schemas:
    Pet:
      type: object
      required: [name]
      properties:
        id: {type: integer}
        name: {type: string, description: The pet's name}
`

func TestOpenAPI(t *testing.T) {
	var gotAuth, gotBody, gotPath, gotTrace string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotTrace = r.Header.Get("Authorization"), r.URL.EscapedPath()+"?"+r.URL.RawQuery, r.Header.Get("X-Trace")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/pets":
			_, _ = io.WriteString(w, `[{"id":1,"name":"Rex"}]`)
		case r.Method == "POST":
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"id":2}`)
		default:
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"message":"no such pet"}`)
		}
	}))
	defer api.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pets.yaml"), []byte(strings.ReplaceAll(petSpec, "BASE", api.URL)), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := OpenAPIConfig{
		Specs: map[string]string{"pets": "pets.yaml"}, Order: []string{"pets"},
		Auth:      map[string]string{"pets": "Bearer s3cret"},
		Workspace: WorkspaceConfig{Dir: dir}, Timeout: 5 * time.Second,
	}
	exec := OpenAPIContext(cfg)
	run := func(args map[string]any) map[string]any {
		t.Helper()
		res, err := exec(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		return res.(map[string]any)
	}

	res := run(map[string]any{"action": "apis"})
	apis, _ := res["apis"].([]map[string]any)
	if len(apis) != 1 || apis[0]["operations"] != 4 || apis[0]["base_url"] != api.URL+"/v1" {
		t.Fatalf("apis %+v", res)
	}
	res = run(map[string]any{"match": "delete a pet"})
	ops, _ := res["operations"].([]map[string]any)
	if len(ops) == 0 || ops[0]["operation"] != "DELETE /pets/{petId}" {
		t.Fatalf("search %+v", res)
	}
	res = run(map[string]any{"action": "describe", "operation": "createPet"})
	body, _ := res["body_schema"].(map[string]any)
	props, _ := body["properties"].(map[string]any)
	if res["body_required"] != true || props["name"] == nil || res["needs_approval"] != true {
		t.Fatalf("describe %+v", res)
	}
	res = run(map[string]any{"action": "describe", "operation": "showPetById"})
	if ps, _ := res["params"].([]map[string]any); len(ps) != 1 || ps[0]["name"] != "petId" || ps[0]["required"] != true {
		t.Fatalf("path-level params %+v", res)
	}

	res = run(map[string]any{"action": "call", "operation": "listPets", "params": map[string]any{"limit": 5.0, "X-Trace": "t1"}})
	if res["status"] != 200 || gotAuth != "Bearer s3cret" || gotPath != "/v1/pets?limit=5" || gotTrace != "t1" {
		t.Fatalf("call %+v auth=%q path=%q", res, gotAuth, gotPath)
	}
	if list, _ := res["body"].([]any); len(list) != 1 {
		t.Fatalf("body %+v", res["body"])
	}
	res = run(map[string]any{"action": "call", "operation": "createPet", "body": map[string]any{"name": "Tom"}})
	if res["status"] != 201 || gotBody != `{"name":"Tom"}` {
		t.Fatalf("post %+v %s", res, gotBody)
	}
	res = run(map[string]any{"action": "call", "operation": "GET /pets/{petId}", "params": map[string]any{"petId": "a/b c"}})
	if gotPath != "/v1/pets/a%2Fb%20c?" || res["error"] != "HTTP 404" {
		t.Fatalf("escaped path %q %+v", gotPath, res)
	}
	for _, bad := range []map[string]any{
		{"action": "call", "operation": "listPets", "params": map[string]any{"bogus": 1}},
		{"action": "call", "operation": "showPetById"},
		{"action": "call", "operation": "showPetById", "params": map[string]any{"petId": ".."}},
		{"action": "call", "operation": "createPet"},
		{"action": "call", "operation": "nope"},
		{"action": "describe", "api": "other"},
	} {
		if res := run(bad); res["error"] == nil {
			t.Errorf("%v: expected an error, got %+v", bad, res)
		}
	}

	gate := OpenAPINeedsApproval(cfg)
	for args, want := range map[string]bool{
		`{"action":"call","operation":"listPets"}`:             false,
		`{"action":"call","operation":"createPet"}`:            true,
		`{"action":"call","operation":"DELETE /pets/{petId}"}`: true,
		`{"action":"call","operation":"unknownOp"}`:            true, // errs closed
		`{"action":"describe","operation":"createPet"}`:        false,
		`{"operation":"createPet"}`:                            false, // default action is a search
		`{"action":"call","api":"missing","operation":"x"}`:    true,
	} {
		var m map[string]any
		_ = json.Unmarshal([]byte(args), &m)
		if got := gate(m); got != want {
			t.Errorf("gate %s = %v want %v", args, got, want)
		}
	}
}

func TestOpenAPISwagger2(t *testing.T) {
	spec := `{"swagger":"2.0","info":{"title":"Old","version":"1"},"host":"api.example.com","basePath":"/api","schemes":["http","https"],
	  "paths":{"/items/{id}":{"put":{"operationId":"putItem","parameters":[
	    {"name":"id","in":"path","required":true,"type":"string"},
	    {"name":"body","in":"body","required":true,"schema":{"$ref":"#/definitions/Item"}}]}}},
	  "definitions":{"Item":{"type":"object","properties":{"name":{"type":"string"}}}}}`
	s, err := parseOpenAPI([]byte(spec), "specs/old.json")
	if err != nil {
		t.Fatal(err)
	}
	if s.BaseURL != "https://api.example.com/api" || len(s.Ops) != 1 {
		t.Fatalf("spec %+v", s)
	}
	op := s.Ops[0]
	if op.ID != "putItem" || !op.BodyReq || op.Body["type"] != "object" || len(op.Params) != 1 {
		t.Fatalf("op %+v", op)
	}
	if _, err := parseOpenAPI([]byte(`{"info":{}}`), "x"); err == nil {
		t.Fatal("non-OpenAPI document accepted")
	}
}
