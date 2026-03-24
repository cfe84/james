#!/bin/sh

VERSION=`cat VERSION`
EXISTS=`git tag | grep "$VERSION"`

if [ -z "$EXISTS" ]; then
  git tag "v$VERSION"
fi
