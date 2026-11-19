module github.com/nilsn3dp/prusa-obico-bridge

go 1.26.11

require (
	github.com/gorilla/websocket v1.5.3
	github.com/prusa3d/prusa-connect/sdk v0.0.0
)

replace github.com/prusa3d/prusa-connect/sdk => ./.core/sdk
