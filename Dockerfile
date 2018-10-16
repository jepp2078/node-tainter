FROM scratch
MAINTAINER Jeppe Johansen <jepp2078@gmail.com>

ADD bin/linux/node-tainter node-tainter

ENTRYPOINT ["/node-tainter"]