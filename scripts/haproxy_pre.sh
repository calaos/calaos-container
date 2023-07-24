#!/bin/bash

set -e

# Init script for Haproxy for calaos-OS
# This script will initialize with a default config if needed and
# generate a self-signed certificates the first time

fs="/mnt/calaos"
tmpdir="/tmp"
certdir="${fs}/haproxy"

mkdir -p ${fs}/haproxy

if [ ! -e "${fs}/haproxy/haproxy.cfg" ]
then
cat > ${fs}/haproxy/haproxy.cfg <<- EOF
#----------------------------------------------------
# Calaos-OS HAProxy config file.
# It supports ssl connection and websocket proxy to
# local calaos_server.
#----------------------------------------------------

global
    log         127.0.0.1 local2

    maxconn     4000
    daemon

    tune.ssl.default-dh-param 2048

defaults
    mode                    http
    log                     global
    option                  httplog
    option                  dontlognull
    option http-server-close
    option forwardfor
    timeout http-request    10s
    timeout queue           1m
    timeout connect         10s
    timeout client          1m
    timeout server          1m
    timeout http-keep-alive 10s
    timeout check           10s
    timeout tunnel          3600s
    maxconn                 3000

frontend calaos-http
    bind *:80
    http-request set-header X-Forwarded-Proto http
    default_backend calaos-backend

frontend calaos-https
    bind *:443 ssl crt /usr/local/etc/haproxy/server.pem
    http-request set-header X-Forwarded-Proto https
    
    default_backend calaos-backend

backend calaos-backend
    #redirect in https if user tries http
    redirect scheme https if !{ ssl_fc }
    server calaos-server 127.0.0.1:5454 check
EOF
fi

if [ -f $certdir/server.pem ] ; then
    echo "certificate already exists."
    echo "To recreate the certificate, delete the file $certdir/server.pem"
    exit 0
fi

#generate https certificate
cat > $tmpdir/cert.cnf << "EOF"
[ req ]
default_bits = 1024
encrypt_key = yes 
distinguished_name = req_dn
x509_extensions = cert_type
prompt = no

[ req_dn ]
C=FR
ST=FRANCE
L=Paris
O=Calaos
OU=Calaos
CN=calaos.fr
emailAddress=none@calaos.fr

[ cert_type ]
basicConstraints                = critical,CA:FALSE
nsCertType                      = server
nsComment                       = "Calaos SSL Certificate"
subjectKeyIdentifier            = hash
authorityKeyIdentifier          = keyid,issuer:always
issuerAltName                   = issuer:copy
keyUsage                        = keyEncipherment, digitalSignature
extendedKeyUsage                = serverAuth
EOF

openssl req -new -outform PEM -config $tmpdir/cert.cnf -out $tmpdir/server.pem -newkey rsa:2048 -nodes -keyout $tmpdir/server.key -keyform PEM -days 9999 -x509

cat $tmpdir/server.pem $tmpdir/server.key > $certdir/server.pem

rm -f $tmpdir/cert.cnf $tmpdir/server.pem $tmpdir/server.key

echo "Successfully generated self-signed certificate"