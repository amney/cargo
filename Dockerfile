FROM node:alpine AS builder

# Create app directory
WORKDIR /usr/src/app

# Install app dependencies
COPY vizceral/package.json .
RUN npm install

# Bundle app source
COPY vizceral/ .

RUN npm run build

FROM golang:latest 

WORKDIR /go/src/cargo
COPY --from=builder /usr/src/app/dist dist
COPY cargo.go .

RUN go get -d .
RUN go build cargo

EXPOSE 8080

CMD ["./cargo"]