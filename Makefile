# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
APP_NAME=repoharvester
SOURCE_NAME=$(APP_NAME).go
VERSION=$(shell git describe --abbrev=0 --tags)

all: clean build-linux build-windows build-osx

clean: 
	rm -f $(APP_NAME).$(VERSION).linux.386.tgz
	rm -f $(APP_NAME).$(VERSION).linux.amd64.tgz
	rm -f $(APP_NAME).$(VERSION).linux.arm64.tgz
	rm -f $(APP_NAME).$(VERSION).linux.arm.tgz
	rm -f $(APP_NAME).$(VERSION).osx.386.tgz
	rm -f $(APP_NAME).$(VERSION).osx.amd64.tgz
	rm -f $(APP_NAME).$(VERSION).windows.386.zip
	rm -f $(APP_NAME).$(VERSION).windows.amd64.zip

build-linux: build-linux-32 build-linux-64 build-linux-arm build-linux-arm64

build-windows: build-windows-32 build-windows-64

build-osx: build-osx-32 build-osx-64 

build-linux-arm:
	GOOS=linux GOARCH=arm $(GOBUILD) -o $(APP_NAME) $(SOURCE_NAME)
	tar zcvf $(APP_NAME).$(VERSION).linux.arm.tgz $(APP_NAME)
	rm -f $(APP_NAME)
build-linux-arm64:
	GOOS=linux GOARCH=arm64 $(GOBUILD) -o $(APP_NAME) $(SOURCE_NAME)
	tar zcvf $(APP_NAME).$(VERSION).linux.arm64.tgz $(APP_NAME)
	rm -f $(APP_NAME)
build-linux-32:
	GOOS=linux GOARCH=386 $(GOBUILD) -o $(APP_NAME) $(SOURCE_NAME)
	tar zcvf $(APP_NAME).$(VERSION).linux.386.tgz $(APP_NAME)
	rm -f $(APP_NAME)
build-linux-64:
	GOOS=linux GOARCH=amd64 $(GOBUILD) -o $(APP_NAME) $(SOURCE_NAME)
	tar zcvf $(APP_NAME).$(VERSION).linux.amd64.tgz $(APP_NAME)
	rm -f $(APP_NAME)

build-windows-32:
	GOOS=windows GOARCH=386 $(GOBUILD) -o $(APP_NAME).exe $(SOURCE_NAME)
	zip $(APP_NAME).$(VERSION).windows.386.zip $(APP_NAME).exe
	rm -f $(APP_NAME).exe
build-windows-64:
	GOOS=windows GOARCH=amd64 $(GOBUILD) -o $(APP_NAME).exe $(SOURCE_NAME)
	zip $(APP_NAME).$(VERSION).windows.amd64.zip $(APP_NAME).exe
	rm -f $(APP_NAME).exe

build-osx-32:
	GOOS=darwin GOARCH=386 $(GOBUILD) -o $(APP_NAME) $(SOURCE_NAME)
	tar zcvf $(APP_NAME).$(VERSION).osx.386.tgz $(APP_NAME)
	rm -f $(APP_NAME)
build-osx-64:
	GOOS=darwin GOARCH=amd64 $(GOBUILD) -o $(APP_NAME) $(SOURCE_NAME)
	tar zcvf $(APP_NAME).$(VERSION).osx.amd64.tgz $(APP_NAME)
	rm -f $(APP_NAME)

build-local:
	$(GOBUILD) -o $(APP_NAME)-$(VERSION) $(SOURCE_NAME)
