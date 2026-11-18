package builtin

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func YamlQuerySchema() Skill {
	return Skill{Name: "yaml_query", Description: "Pick values out of YAML (Kubernetes manifests, docker-compose, CI configs, Ansible) with the same " +
		"jq-style expressions as json_query. Multi-document YAML becomes an array of documents. Reads inline text or a workspace file.",
		Parameters: map[string]Param{
			"yaml":   {Type: "string", Description: "The YAML text (or give file)", Required: false},
			"file":   {Type: "string", Description: "Workspace path of a YAML file", Required: false},
			"query":  {Type: "string", Description: "Expression, e.g. .services | keys  or  .[] | select(.kind == \"Deployment\") | .metadata.name", Required: false},
			"output": {Type: "string", Description: "json (default) or yaml — yaml also returns the result rendered as YAML text", Required: false},
		}}
}

func YamlQuery(ws WorkspaceConfig) Executor {
	jq := JsonQuery()
	return func(args map[string]any) (any, error) {
		src := str(args, "yaml")
		if f := strings.TrimSpace(str(args, "file")); f != "" {
			b, err := readWorkspaceFile(ws, f, maxBodyBytes)
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			src = string(b)
		}
		if strings.TrimSpace(src) == "" {
			return map[string]any{"error": "yaml or file is required"}, nil
		}
		if len(src) > maxBodyBytes {
			return map[string]any{"error": "yaml is larger than 2 MiB"}, nil
		}
		docs, err := decodeYAMLDocs(src)
		if err != nil {
			return map[string]any{"error": "invalid YAML: " + err.Error()}, nil
		}
		var doc any = docs
		if len(docs) == 1 {
			doc = docs[0]
		}
		res, err := jq(map[string]any{"json": doc, "query": str(args, "query")})
		if err != nil {
			return nil, err
		}
		m, ok := res.(map[string]any)
		if !ok || m["error"] != nil {
			return res, nil
		}
		if len(docs) > 1 {
			m["documents"] = len(docs)
		}
		if strings.EqualFold(strings.TrimSpace(str(args, "output")), "yaml") {
			var v any = m["results"]
			if r, ok := m["result"]; ok {
				v = r
			}
			var buf bytes.Buffer
			enc := yaml.NewEncoder(&buf)
			enc.SetIndent(2)
			if err := enc.Encode(v); err == nil {
				m["yaml"] = buf.String()
			}
		}
		return m, nil
	}
}

// decodeYAMLDocs decodes every document into JSON-shaped values, which is
// what the jq engine walks: map[string]any, []any, float64, string, bool, nil.
func decodeYAMLDocs(src string) ([]any, error) {
	dec := yaml.NewDecoder(strings.NewReader(src))
	var out []any
	for i := 0; i < 1000; i++ {
		var v any
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if v == nil {
			continue // empty document ("---" at the end)
		}
		out = append(out, jsonShape(v))
	}
	if len(out) == 0 {
		return nil, errors.New("no documents")
	}
	return out, nil
}

func jsonShape(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = jsonShape(e)
		}
		return x
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[fmt.Sprint(k)] = jsonShape(e)
		}
		return m
	case []any:
		for i, e := range x {
			x[i] = jsonShape(e)
		}
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case uint64:
		return float64(x)
	case float32:
		return float64(x)
	case time.Time:
		return x.Format(time.RFC3339)
	default:
		return x
	}
}

// readWorkspaceFile reads at most max bytes of a workspace file.
func readWorkspaceFile(ws WorkspaceConfig, p string, max int) ([]byte, error) {
	root, err := ws.open()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel, err := relPath(p)
	if err != nil {
		return nil, err
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > max {
		return nil, fmt.Errorf("%s is larger than %d KiB", p, max>>10)
	}
	return b, nil
}
