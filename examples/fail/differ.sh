#!/bin/sh -x

# WARNING this script is just a crude example, there are many cases that will
# cause issues (like spaces in files names)

# Paths to the directories
originalPath="$1"
modifiedPath="$2"
changesPath="$3"

# Create the changes directory if it doesn't exist
mkdir -p "$changesPath"

# Find the differences between directories original and modified
diffs=$(diff -r "$originalPath" "$modifiedPath" | grep -v "Only in $originalPath")

# Copy the differences to the changes directory
while read -r line; do
    file=$(echo "$line" | awk '{print $4}')
    dest="$changesPath/$file"

    # Create the directory structure if it doesn't exist
    mkdir -p "$(dirname "$dest")"

    # Copy the file to the changes directory, preserving symlinks
    cp -a "$modifiedPath/$file" "$dest"
done <<EOF
$diffs
EOF
