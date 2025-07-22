all: build deploy 

build:
	go build

deploy:
	scp mailreceiver config.yaml mail.masa23.jp:/tmp/
	ssh mail.masa23.jp "install -m 755 -o test -g test /tmp/mailreceiver /home/test/mailreceiver && install -m 644 -o test -g test /tmp/config.yaml /home/test/config.yaml" 
