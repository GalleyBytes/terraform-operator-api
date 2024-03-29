#!/bin/bash

OUTPUT_FILE="$(mktemp)"
TFO_BUNDLE="$(mktemp)"

printf 'apiVersion: v1
kind: List
metadata:
  resourceVersion: ""
  selfLink: ""

items:' >"$OUTPUT_FILE"

helm template tfo-virtual-cluster vcluster --repo https://charts.loft.sh -n namespace-placeholder |
    sed 's/^---//g' |
    sed 's/namespace-placeholder/\"{{ .namespace }}\"/g' |
    sed "s/^/  /g" |
    sed "s/^  #/- #/g" >>"$OUTPUT_FILE"

curl -s https://raw.githubusercontent.com/GalleyBytes/terraform-operator/master/deploy/bundles/v0.13.3/v0.13.3.yaml |
    sed "s/^/      /g" >"$TFO_BUNDLE"



printf "
📄\tTFO bundle saved at $TFO_BUNDLE

📑\tVCluster manifest saved at $OUTPUT_FILE

Insert the TFO bundle under the VCluster's init--configmap 'data.manifests: |-'
"