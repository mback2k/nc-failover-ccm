package main

//go:generate wget https://www.servercontrolpanel.de/SCP/WSEndUser?wsdl -O nc/scp/WSEndUser.wsdl
//go:generate sed -i "" -e "s/:443//" nc/scp/WSEndUser.wsdl
//go:generate go run github.com/hooklift/gowsdl/cmd/gowsdl -d nc -p scp -o api.go nc/scp/WSEndUser.wsdl
//go:generate ./generate.sh
//go:generate go build "-ldflags=-s -w"
