#!/bin/sh

VERSION=`cat VERSION`
EXISTS=`git tag | grep "$VERSION"`

if [ -z "$EXISTS" ]; then
  echo "Create tag v$VERSION"
  git tag "v$VERSION"
fi
