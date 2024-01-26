configPath:=config/config.yaml

.PHONE: build
build:
	CGO_ENABLED=0 go build -o ./output/app ${CMD_DIR}

.PHONE: exec
exec:
	./output/app --conf-path ${configPath}

.PHONE: run
run: build
	./output/app --conf-path ${configPath}

run-%: build
	./output/app --conf-path config/config_$*.yaml
