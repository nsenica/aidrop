#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)

if [ -z "${AIDROP_BEARER_TOKEN:-}" ]; then
	printf '%s\n' 'AIDROP_BEARER_TOKEN must be set before running the container smoke check.' >&2
	exit 1
fi
if ! command -v docker >/dev/null 2>&1; then
	printf '%s\n' 'docker is required to run the container smoke check.' >&2
	exit 1
fi
if ! command -v curl >/dev/null 2>&1; then
	printf '%s\n' 'curl is required to run the container smoke check.' >&2
	exit 1
fi

project_name="aidrop-smoke-$$"
fixture_value='aidrop-volume-survived-recreate'
smoke_port=${AIDROP_SMOKE_PORT:-$((20000 + ($$ % 40000)))}
temporary_dir=$(mktemp -d)
response_file="$temporary_dir/response"
original_file="$temporary_dir/original.html"
replacement_file="$temporary_dir/replacement.html"
invalid_zip_file="$temporary_dir/invalid.zip"
recovery_file="$temporary_dir/recovery.html"
base_url="http://127.0.0.1:$smoke_port"

printf '%s\n' '<h1>smoke original</h1>' > "$original_file"
printf '%s\n' '<h1>smoke replacement</h1>' > "$replacement_file"
printf '%s\n' 'this is not a ZIP archive' > "$invalid_zip_file"
printf '%s\n' '<h1>recovered after failed upload</h1>' > "$recovery_file"

export AIDROP_BIND_ADDRESS=127.0.0.1
export AIDROP_PORT=$smoke_port

compose() {
	docker compose --project-name "$project_name" --file "$repo_dir/compose.yaml" "$@"
}

cleanup() {
	compose down --volumes --remove-orphans >/dev/null 2>&1 || true
	rm -rf "$temporary_dir"
}

expect_http_status() {
	expected_status=$1
	method=$2
	path=$3
	content_type=$4
	data_file=$5
	if [ "$data_file" = '-' ]; then
		actual_status=$(curl --silent --show-error --output "$response_file" --write-out '%{http_code}' \
			--request "$method" --header "Authorization: Bearer $AIDROP_BEARER_TOKEN" "$base_url$path")
	else
		actual_status=$(curl --silent --show-error --output "$response_file" --write-out '%{http_code}' \
			--request "$method" --header "Authorization: Bearer $AIDROP_BEARER_TOKEN" \
			--header "Content-Type: $content_type" --data-binary "@$data_file" "$base_url$path")
	fi
	if [ "$actual_status" != "$expected_status" ]; then
		printf 'expected HTTP %s for %s %s, got %s: ' "$expected_status" "$method" "$path" "$actual_status" >&2
		cat "$response_file" >&2
		exit 1
	fi
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

compose up --build --detach --force-recreate --wait --wait-timeout 60
compose exec --no-TTY --user 10001:10001 aidrop sh -ec 'printf "%s\n" "$1" > /data/.container-smoke-fixture' sh "$fixture_value"

expect_http_status 201 POST /api/v1/projects/smoke/artifacts/restore text/html "$original_file"
expect_http_status 400 POST /api/v1/projects/smoke/artifacts/recovery application/zip "$invalid_zip_file"
expect_http_status 404 GET /api/v1/projects/smoke/artifacts/recovery '' -
expect_http_status 201 POST /api/v1/projects/smoke/artifacts/recovery text/html "$recovery_file"
expect_http_status 400 PUT /api/v1/projects/smoke/artifacts/restore application/zip "$invalid_zip_file"
expect_http_status 200 GET /api/v1/projects/smoke/artifacts/restore/source '' -
if ! cmp -s "$original_file" "$response_file"; then
	printf '%s\n' 'failed replacement changed the original artifact source.' >&2
	exit 1
fi
expect_http_status 200 PUT /api/v1/projects/smoke/artifacts/restore text/html "$replacement_file"
expect_http_status 200 GET /api/v1/projects/smoke/artifacts/restore/source '' -
if ! cmp -s "$replacement_file" "$response_file"; then
	printf '%s\n' 'valid replacement did not recover after the failed replacement.' >&2
	exit 1
fi

compose up --build --detach --force-recreate --wait --wait-timeout 60

actual=$(compose exec --no-TTY --user 10001:10001 aidrop cat /data/.container-smoke-fixture)
if [ "$actual" != "$fixture_value" ]; then
	printf '%s\n' 'persistent volume fixture did not survive container recreation.' >&2
	exit 1
fi

expect_http_status 200 GET /api/v1/projects/smoke/artifacts/restore/source '' -
if ! cmp -s "$replacement_file" "$response_file"; then
	printf '%s\n' 'artifact source did not survive container recreation.' >&2
	exit 1
fi

compose stop aidrop
compose run --rm --no-deps -T --entrypoint tar aidrop -czf - -C /data . > "$temporary_dir/backup.tar.gz"
compose down --volumes --remove-orphans
cat "$temporary_dir/backup.tar.gz" | compose run --rm --no-deps -i -T --entrypoint tar aidrop -xzf - -C /data
compose up --build --detach --force-recreate --wait --wait-timeout 60
expect_http_status 200 GET /api/v1/projects/smoke/artifacts/restore/source '' -
if ! cmp -s "$replacement_file" "$response_file"; then
	printf '%s\n' 'artifact source did not survive backup and restore.' >&2
	exit 1
fi

printf '%s\n' 'container persistence, backup/restore, and failed-upload recovery smoke check passed.'
