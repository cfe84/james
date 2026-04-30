#!/bin/sh

VERSION=`cat VERSION`
TAG=`v$VERSION`
EXISTS=`git tag | grep "$TAG"`

if [ -z "$EXISTS" ]; then
  echo "Create tag $TAG"
  git tag "$TAG"
else
  echo "Tag $TAG already exists"
fi
