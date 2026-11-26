module github.com/ivedhahpc/hxdna/provision

go 1.26.3

require github.com/ivedhahpc/hxdna v1.27.0

require (
	github.com/go-resty/resty/v2 v2.17.2 // indirect
	github.com/joho/godotenv v1.5.1 // indirect
	github.com/klauspost/compress v1.18.5 // indirect
	github.com/nats-io/nats.go v1.52.0 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	go.uber.org/zap v1.28.0 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
)

// Built against the hxdna next to it in this repo. Consumers ignore replace directives and use
// the hxdna version required above — tag hxdna v1.27.0 before tagging provision/v0.1.0.
replace github.com/ivedhahpc/hxdna => ../
