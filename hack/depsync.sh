#!/usr/bin/env bash

set -eo pipefail

go install go.k6.io/xk6@latest

CHANGES=$(xk6 sync 2>&1 | grep "Sync module=" | sed -E 's/.*Sync module=//')

if [[ -z $CHANGES ]]; then
    echo "Nothing to do."
    exit 0
fi

cat <<EOF > sync-pr-body.txt
This automated PR aligns the following dependency mismatches with k6 core:
\`\`\`
$(echo -e "$CHANGES")
\`\`\`

Due to a limitation of GitHub Actions, to run CI checks for this PR, close it and reopen it again.
EOF
