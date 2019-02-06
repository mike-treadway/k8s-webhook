FROM golang:1.9 as builder
RUN go get -d github.com/newrelic/nri-mysql/... && \
    cd /go/src/github.com/newrelic/nri-mysql && \
    make && \
    strip ./bin/nr-mysql

FROM newrelic/infrastructure:latest
COPY --from=builder /go/src/github.com/newrelic/nri-mysql/bin/nr-mysql /var/db/newrelic-infra/newrelic-integrations/bin/nr-mysql

# integrations              latest              7774cf9bfbaf        9 seconds ago       24.3MB

# CONTAINER ID  NAME                                                                                                    CPU %    MEM USAGE / LIMIT     MEM %               NET I/O             BLOCK I/O           PIDS
# 901e3c71fc11  k8s_newrelic-sidecar_mysql-deployment-58c8f987f5-b2mgj_default_8228fd70-0520-11e9-b25c-08002701937e_0   0.23%    10.69MiB / 1.944GiB   0.54%               127kB / 113kB       0B / 0B             9

# cpu usage max 2.3%
# cpu avg 0.181505