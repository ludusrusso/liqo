FROM alpine:3.9.5
RUN apk add --update curl bash openssl
RUN curl -LO https://storage.googleapis.com/kubernetes-release/release/v1.17.0/bin/linux/amd64/kubectl && chmod +x ./kubectl && cp kubectl /usr/bin/kubectl

COPY scripts/secret-creation/secret-creation.sh /usr/bin/local/secret-creation.sh
RUN mkdir /root/.kube
ENTRYPOINT [ "/usr/bin/local/secret-creation.sh" ]
