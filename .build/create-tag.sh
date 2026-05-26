#!/bin/sh

VERSION=`cat VERSION`
TAG="v$VERSION"
EXISTS=`git tag | grep "$TAG"`

if [ -z "$EXISTS" ]; then
  echo "Create tag $TAG"
  git tag "$TAG"
  for REMOTE in `git remote`; do
    git push $REMOTE --tags
  done
else
  echo "Tag $TAG already exists"
fi
