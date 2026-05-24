.PHONY: build-win integration-test

build-win:
	go test -v ./...
# 	bus
	rm -f app/cmd/bus/resource.syso
	cd app/cmd/bus && goversioninfo -64 versioninfo.json
	go build -o bus.exe ./app/cmd/bus
	rm -f app/cmd/bus/resource.syso
	
integration-test:
	WS_WAIT_SECONDS=6 go test -count=1 -p=1 -tags=integration ./... -v 2>&1
	