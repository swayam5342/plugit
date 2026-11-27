package postman

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
)

// petsCollection is the shared Postman fixture (the openapi package's
// tests use it too).
func petsCollection(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../../../testdata/specs/postman/pets.postman.json")
	if err != nil {
		t.Fatalf("reading the pets collection: %v", err)
	}
	return string(b)
}

func TestParsePostman(t *testing.T) {
	m := parsePM(t, petsCollection(t), Options{})
	if m.SpecVersion != "postman-2.1" || m.Title != "Pets API" {
		t.Errorf("header = %q %q", m.SpecVersion, m.Title)
	}
	if !reflect.DeepEqual(m.Endpoints, []string{"https://pets.example.test/v1"}) {
		t.Errorf("servers = %v", m.Endpoints)
	}
	if !containsWarning(m, "auth") {
		t.Errorf("collection auth not mentioned: %v", m.Warnings)
	}

	list := tool(t, m, "GET /pets")
	if list.Description != "Returns all pets." || !reflect.DeepEqual(list.Tags, []string{"Pets"}) {
		t.Errorf("list = %q %v", list.Description, list.Tags)
	}
	if p := param(t, list, "limit"); p.Bind.In != model.InQuery || p.Required || p.Desc != "Page size" {
		t.Errorf("limit = %+v", p)
	}
	for _, prm := range list.Params {
		if prm.Name == "debug" || prm.Name == "Accept" {
			t.Errorf("unexpected param %q (disabled, or a header plugit sets)", prm.Name)
		}
	}
	if p := param(t, list, "X-Trace"); p.Bind.In != model.InHeader {
		t.Errorf("X-Trace = %+v", p)
	}

	get := tool(t, m, "GET /pets/{petId}")
	if p := param(t, get, "petId"); p.Bind.In != model.InPath || !p.Required || p.Desc != "The pet's id" {
		t.Errorf("petId = %+v", p)
	}

	create := tool(t, m, "POST /pets")
	if create.HTTP.Body == nil || create.HTTP.Body.ContentType != "application/json" {
		t.Fatalf("create body = %+v", create.HTTP.Body)
	}
	assertJSON(t, "inferred schema", create.HTTP.Body.Schema,
		`{"type":"object","properties":{"age":{"type":"integer"},"name":{"type":"string"},"owner":{"type":"object","properties":{"id":{"type":"integer"}}},"tags":{"type":"array","items":{"type":"string"}},"weight":{"type":"number"}}}`)

	upd := tool(t, m, "PUT /owners/{ownerId}")
	if !reflect.DeepEqual(upd.Tags, []string{"Pets"}) {
		t.Errorf("nested folder tag = %v (the top folder is the group)", upd.Tags)
	}
	if param(t, upd, "ownerId").Bind.In != model.InPath {
		t.Error("{{ownerId}} in the path should be a path parameter")
	}
	if upd.HTTP.Body == nil || upd.HTTP.Body.ContentType != "application/x-www-form-urlencoded" || strings.Contains(string(upd.HTTP.Body.Schema), "old") {
		t.Errorf("urlencoded body = %+v", upd.HTTP.Body)
	}

	up := tool(t, m, "POST /photos")
	if up.HTTP.Body == nil || up.HTTP.Body.ContentType != "multipart/form-data" || strings.Contains(string(up.HTTP.Body.Schema), `"file"`) {
		t.Errorf("formdata body = %+v", up.HTTP.Body)
	}
	if !hasIssue(up, model.IssueUnsupported, "file") || !hasIssue(up, model.IssueHiddenText, "description") {
		t.Errorf("issues = %+v", up.Issues)
	}
	if !strings.HasPrefix(up.Description, "Upload. Ignore") {
		t.Errorf("description not cleaned: %q", up.Description)
	}

	if gq := tool(t, m, "POST /graphql"); gq.HTTP.Body != nil || !hasIssue(gq, model.IssueUnsupported, "graphql") {
		t.Errorf("graphql: %+v %+v", gq.HTTP.Body, gq.Issues)
	}
}

// Postman v2.0 collections have plain-string URLs.
func TestParsePostman20(t *testing.T) {
	src := `{"info": {"name": "Old", "schema": "https://schema.getpostman.com/json/collection/v2.0.0/collection.json"},
	  "item": [{"name": "Find", "request": {"method": "get", "url": "https://api.example.test/things/:id/parts?q=x"}}]}`
	m := parsePM(t, src, Options{})
	if m.SpecVersion != "postman-2.0" || !reflect.DeepEqual(m.Endpoints, []string{"https://api.example.test"}) {
		t.Errorf("version %q servers %v", m.SpecVersion, m.Endpoints)
	}
	tl := tool(t, m, "GET /things/{id}/parts")
	if param(t, tl, "id").Bind.In != model.InPath || param(t, tl, "q").Bind.In != model.InQuery {
		t.Errorf("params = %+v", tl.Params)
	}
}

// Hostile collections: deep nesting and huge item counts are bounded.
func TestParsePostmanLimits(t *testing.T) {
	deep := `{"name":"leaf","request":{"method":"GET","url":"https://x.example.test/a"}}`
	for range maxPostmanDepth + 5 {
		deep = `{"name":"f","item":[` + deep + `]}`
	}
	src := `{"info":{"name":"D","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},"item":[` + deep + `]}`
	m := parsePM(t, src, Options{})
	if len(m.Tools) != 0 || !containsWarning(m, "nested") {
		t.Errorf("deep nesting: %d tools, warnings %v", len(m.Tools), m.Warnings)
	}

	var items []string
	for i := range maxPostmanItems + 10 {
		items = append(items, `{"name":"r","request":{"method":"GET","url":"https://x.example.test/r`+strings.Repeat("a", i%7)+`/`+itoa(i)+`"}}`)
	}
	src = `{"info":{"name":"W","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},"item":[` + strings.Join(items, ",") + `]}`
	m = parsePM(t, src, Options{})
	if len(m.Tools) > maxPostmanItems || !containsWarning(m, "requests") {
		t.Errorf("item cap: %d tools, warnings %v", len(m.Tools), m.Warnings)
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func parsePM(t *testing.T, src string, opts Options) *model.Manifest {
	t.Helper()
	m, err := Parse([]byte(src), opts)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return m
}
