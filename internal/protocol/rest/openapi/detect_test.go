package openapi

import "testing"

func TestIsOpenAPI(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want bool
	}{
		{"json openapi", `{"openapi":"3.0.0","info":{"title":"t","version":"1"},"paths":{}}`, true},
		{"json swagger", `{"swagger":"2.0","info":{"title":"t","version":"1"},"paths":{}}`, true},
		{"json key after others", `{"info":{"title":"t","version":"1"},"paths":{"/x":{}},"openapi":"3.1.0"}`, true},
		{"json bom and whitespace", "\xef\xbb\xbf \r\n\t{\"openapi\":\"3.0.0\"}", true},
		{"json nested only", `{"info":{"openapi":"3.0.0","swagger":"2.0"},"paths":{}}`, false},
		{"json array", `[{"openapi":"3.0.0"}]`, false},
		{"json key as value", `{"x":"openapi","y":["swagger"]}`, false},
		{"json malformed before key", `{"info":{,"openapi":"3.0.0"}`, false},
		{"yaml openapi", "openapi: 3.0.3\ninfo: {title: t, version: '1'}\npaths: {}\n", true},
		{"yaml swagger", "swagger: '2.0'\ninfo: {title: t, version: '1'}\npaths: {}\n", true},
		{"yaml double quoted", "\"openapi\": 3.0.3\n", true},
		{"yaml single quoted", "'swagger': '2.0'\n", true},
		{"yaml key at end of line", "openapi:\n  3.0.3\n", true},
		{"yaml crlf", "info:\r\n  title: t\r\nopenapi: 3.0.3\r\n", true},
		{"yaml preamble", "\xef\xbb\xbf%YAML 1.2\n---\n# a comment\n\nopenapi: 3.1.0\n", true},
		{"yaml key after others", "info:\n  title: t\npaths: {}\nopenapi: 3.0.0\n", true},
		{"yaml nested only", "info:\n  openapi: 3.0.0\n  swagger: '2.0'\npaths: {}\n", false},
		{"yaml prefix of key", "openapiVersion: 3.0.0\nswaggerx: '2.0'\n", false},
		{"yaml no colon", "openapi 3.0.0\n", false},
		{"yaml colon without space", "openapi:3.0.0\n", false},
		{"yaml in comment", "# openapi: 3.0.0\nasyncapi: 2.6.0\n", false},
		{"asyncapi", "asyncapi: 2.6.0\ninfo: {title: t, version: '1'}\nchannels: {}\n", false},
		{"graphql sdl", "type Query {\n  user(id: ID!): User\n}\n\ntype User {\n  id: ID!\n  openapi: String\n}\n", false},
		{"graphql introspection", `{"data":{"__schema":{"queryType":{"name":"Query"},"types":[{"kind":"OBJECT","name":"openapi"}]}}}`, false},
		{"postman", `{"info":{"name":"c","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},"item":[]}`, false},
		{"empty", "", false},
		{"bom only", "\xef\xbb\xbf", false},
		{"garbage", "\x00\xff\xfe random { text: ] \"openapi", false},
		{"unterminated json", `{"openapi`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsOpenAPI([]byte(tc.data)); got != tc.want {
				t.Errorf("IsOpenAPI(%q) = %v, want %v", tc.data, got, tc.want)
			}
		})
	}
}

// IsOpenAPI runs on untrusted bytes in the isolate child before any parser:
// it must never panic.
func FuzzIsOpenAPI(f *testing.F) {
	addSeeds(f)
	f.Add([]byte(`{"openapi":`))
	f.Add([]byte("'openapi: \n"))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_ = IsOpenAPI(data)
	})
}
