#! /usr/bin/bash

ko delete --ignore-not-found=true \
  -Rf config/core/ \
  -f ./third_party/kourier-latest/kourier.yaml \
  -f ./third_party/cert-manager-latest/cert-manager.yaml \
  -f https://projectcontour.io/quickstart/contour.yaml
