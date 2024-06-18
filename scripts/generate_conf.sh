#!/bin/bash

read -p ">.env (DOMAIN): " DOMAIN
read -p ">.env (EMAIL): " EMAIL
read -p ">.env (MCINFRAMANAGER): " MCINFRAMANAGER
read -p ">.env (MCINFRAMANAGER_APIUSERNAME): " MCINFRAMANAGER_APIUSERNAME
read -p ">.env (MCINFRAMANAGER_APIPASSWORD): " MCINFRAMANAGER_APIPASSWORD

if [ -z "$DOMAIN" ] || [ -z "$EMAIL" ]; then
    echo -e "================================================"
    echo "Use existing settings because there is no input."

    set -o allexport
    source .env
    set +o allexport

    if [ -z "$DOMAIN" ] || [ -z "$EMAIL" ]; then
        echo -e "================================================"
        echo "DOMAIN and EMAIL must be set in the .env file."
        exit 1
    fi
fi

echo "DOMAIN=${DOMAIN}" > .env
echo "EMAIL=${EMAIL}" >> .env
echo "MCINFRAMANAGER=${MCINFRAMANAGER}" >> .env
echo "MCINFRAMANAGER_APIUSERNAME=${MCINFRAMANAGER_APIUSERNAME}" >> .env
echo "MCINFRAMANAGER_APIPASSWORD=${MCINFRAMANAGER_APIPASSWORD}" >> .env

echo -e "================================================"
echo -e " * DOMAIN = ${DOMAIN}\n * EMAIL = ${EMAIL}"
echo -e "================================================"



mkdir -p ./nginx
cat > ./nginx/nginx.conf <<EOL
events {}

http {
    server {
        listen 5000 ssl;
        server_name ${DOMAIN};

        ssl_certificate /etc/letsencrypt/live/${DOMAIN}/fullchain.pem;
        ssl_certificate_key /etc/letsencrypt/live/${DOMAIN}/privkey.pem;

        location / {
            proxy_pass http://mciammanager:3000;
            proxy_set_header Host \$host;
            proxy_set_header X-Real-IP \$remote_addr;
            proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto \$scheme;
        }
    }

    server {
        listen 443 ssl;
        server_name ${DOMAIN};

        ssl_certificate /etc/letsencrypt/live/${DOMAIN}/fullchain.pem;
        ssl_certificate_key /etc/letsencrypt/live/${DOMAIN}/privkey.pem;

        location / {
            proxy_pass https://keycloak:8443;
            proxy_set_header Host \$host;
            proxy_set_header X-Real-IP \$remote_addr;
            proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto \$scheme;
        }
    }

    server {
        listen 80;
        server_name ${DOMAIN};

        location / {
            proxy_pass http://keycloak:8080;
            proxy_set_header Host \$host;
            proxy_set_header X-Real-IP \$remote_addr;
            proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto \$scheme;
        }
    }
}

EOL

cat > ./nginx/nginx-cert.conf <<EOL
events {}

http {
    server {
        listen 80;
        server_name ${DOMAIN};

        location /.well-known/acme-challenge/ {
            root /var/www/certbot;
            allow all;
        }

        location / {
            return 301 https://\$host\$request_uri;
        }
    }
}
EOL

echo 
echo "** Nginx configuration file has been created at ./nginx/nginx.conf **"
echo 