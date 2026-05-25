// Example: register a JSON endpoint with stdlib net/http and describe it in
// an OpenAPI spec separately. The gone/openapi package only builds the spec
// and serves /openapi.json + /docs; route registration stays plain
// http.ServeMux.
package main

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/tmshlvck/gone/openapi"
)

type helloReq struct {
	Name string `path:"name" example:"world"`
}

type helloResp struct {
	Hello string `json:"hello" example:"world"`
}

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /hello/{name}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(helloResp{Hello: r.PathValue("name")})
	})

	spec := openapi.NewSpec("gone openapi example", "0.1.0")
	spec.Add("GET", "/hello/{name}", openapi.Op{
		Summary: "Greet by name",
		Tags:    []string{"hello"},
		Req:     new(helloReq),
		Resp:    new(helloResp),
	})
	spec.Mount(mux, "/openapi.json", "/docs")

	addr := ":8080"
	log.Printf("openapi example listening on %s — try /hello/Tomas, /openapi.json, /docs", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
