#!/bin/bash
set -e

docker cp nginx:/run/nginx.pid ./nginx.pid
docker cp ./nginx.conf nginx:/etc/nginx/nginx.conf
docker exec nginx nginx -s reload

docker cp ./nginx.pid nginx:/run/nginx.pid
rm ./nginx.pid
