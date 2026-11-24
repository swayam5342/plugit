package formats

import "testing"

const openapiDoc = `{"openapi":"3.0.0","info":{"title":"t","version":"1"},
"servers":[{"url":"https://api.example.test"}],
"paths":{"/pets":{"get":{"operationId":"listPets","responses":{"200":{"description":"ok"}}}}}}`

const postmanDoc = `{"info":{"name":"c","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
"item":[{"name":"list","request":{"method":"GET","url":{"raw":"https://api.example.test/pets","host":["api","example","test"],"path":["pets"]}}}]}`

func TestOrderedPairwiseDetection(t *testing.T) {
	for _, data := range []string{openapiDoc, postmanDoc} {
		n := 0
		for _, f := range Ordered() {
			if f.Detect([]byte(data)) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("fixture detected by %d formats, want exactly 1", n)
		}
	}
}
