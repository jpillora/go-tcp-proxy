FROM golang:1.4-onbuild

ENTRYPOINT ["go-wrapper", "run"]
