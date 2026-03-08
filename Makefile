# Cygctl Makefile
# Usage:
#   make all      - Build all components
#   make install  - Build and install to Cygwin bin
#   make clean    - Remove built files

GO = go
GOFLAGS = -ldflags="-s -w"

CYGWIN_BIN = C:/cygwin64/bin

all: cygctl apt-cyg sudo su

cygctl:
	$(GO) build $(GOFLAGS) -o cygctl.exe .

apt-cyg:
	cd cmd/apt-cyg && $(GO) build $(GOFLAGS) -o apt-cyg.exe .

sudo:
	cd cmd/sudo && $(GO) build $(GOFLAGS) -o sudo.exe .

su:
	cd cmd/su && $(GO) build $(GOFLAGS) -o su.exe .

install: all
	cp cygctl.exe $(CYGWIN_BIN)/cygctl.exe
	cp cmd/apt-cyg/apt-cyg.exe $(CYGWIN_BIN)/apt-cyg.exe
	cp cmd/sudo/sudo.exe $(CYGWIN_BIN)/sudo.exe
	cp cmd/su/su.exe $(CYGWIN_BIN)/su.exe
	@echo "Installed to $(CYGWIN_BIN)"
	@echo "Run 'powershell -File install.ps1' to configure aliases"

clean:
	rm -f cygctl.exe cmd/apt-cyg/apt-cyg.exe cmd/sudo/sudo.exe cmd/su/su.exe

.PHONY: all cygctl apt-cyg sudo su install clean
