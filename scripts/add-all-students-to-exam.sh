#!/usr/bin/env bash
set -Eeuo pipefail

# Add every enabled non-platform user to an exam's participant roster. The
# operation is idempotent: existing participant rows are left unchanged.
# This intentionally uses the database pod so it works without an admin cookie
# or an externally exposed API endpoint.

usage() {
  cat >&2 <<'EOF'
Usage:
  add-all-students-to-exam.sh [options] <exam-uuid>

Options:
  --apply                 Insert missing students (without this flag, preview only)
  --dry-run               Explicitly request preview mode (default)
  -n, --namespace NAME    Kubernetes namespace (default: $BYOD_NAMESPACE or byod)
  -h, --help              Show this help

Environment overrides:
  BYOD_ISSUER             User issuer (default: https://connect.cs.ac.cn)
  BYOD_DB_SECRET          PostgreSQL app Secret (default: byod-server-postgres-app)
  BYOD_DB_NAME            Database name (default: byod-server)
  BYOD_DB_HOST            PostgreSQL service DNS name
  BYOD_DB_POD_SELECTOR    Primary PostgreSQL pod selector

Examples:
  # Preview how many users are missing:
  ./scripts/add-all-students-to-exam.sh 2fad0191-a522-4f23-8607-1a1717ebdbdd

  # Add the missing users:
  ./scripts/add-all-students-to-exam.sh --apply 2fad0191-a522-4f23-8607-1a1717ebdbdd
EOF
}

die() {
  echo "ERROR: $*" >&2
  exit 1
}

namespace="${BYOD_NAMESPACE:-byod}"
issuer="${BYOD_ISSUER:-https://connect.cs.ac.cn}"
db_secret="${BYOD_DB_SECRET:-byod-server-postgres-app}"
db_name="${BYOD_DB_NAME:-byod-server}"
db_selector="${BYOD_DB_POD_SELECTOR:-cnpg.io/cluster=byod-server-postgres,cnpg.io/instanceRole=primary}"
apply=false
exam_id=""

while (($# > 0)); do
  case "$1" in
    --apply)
      apply=true
      shift
      ;;
    --dry-run)
      apply=false
      shift
      ;;
    -n|--namespace)
      (($# >= 2)) || die "--namespace requires a value"
      namespace="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --*)
      die "unknown option: $1"
      ;;
    *)
      [[ -z "$exam_id" ]] || die "only one exam UUID may be supplied"
      exam_id="$1"
      shift
      ;;
  esac
done

[[ -n "$exam_id" ]] || { usage; exit 2; }
[[ "$exam_id" =~ ^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$ ]] \
  || die "exam ID must be a UUID: $exam_id"
command -v kubectl >/dev/null 2>&1 || die "kubectl is required"
command -v base64 >/dev/null 2>&1 || die "base64 is required"

pod="$(kubectl -n "$namespace" get pods -l "$db_selector" -o jsonpath='{.items[0].metadata.name}')"
[[ -n "$pod" ]] || die "no PostgreSQL primary pod found in namespace $namespace (selector: $db_selector)"

db_host="${BYOD_DB_HOST:-byod-server-postgres-rw.${namespace}.svc.cluster.local}"
db_user="$(kubectl -n "$namespace" get secret "$db_secret" -o jsonpath='{.data.username}' | base64 -d)"
db_password="$(kubectl -n "$namespace" get secret "$db_secret" -o jsonpath='{.data.password}' | base64 -d)"
[[ -n "$db_user" && -n "$db_password" ]] || die "Secret $namespace/$db_secret has no username/password"
trap 'unset db_password' EXIT

psql_exec() {
  kubectl -n "$namespace" exec -i "$pod" -- env PGPASSWORD="$db_password" \
    psql -X -v ON_ERROR_STOP=1 -h "$db_host" -U "$db_user" -d "$db_name" "$@"
}

exam_info="$(psql_exec -At -F $'\t' -v exam_id="$exam_id" <<'SQL'
SELECT name, hashtag, state
FROM byod_exams
WHERE id = :'exam_id';
SQL
)"
[[ -n "$exam_info" ]] || die "exam not found: $exam_id"
IFS=$'\t' read -r exam_name exam_hashtag exam_state <<< "$exam_info"

counts="$(psql_exec -At -F $'\t' -v exam_id="$exam_id" -v issuer="$issuer" <<'SQL'
WITH candidates AS (
  SELECT u.id AS user_id
  FROM byod_users u
  WHERE u.issuer = :'issuer'
    AND u.enabled
    AND NOT u.platform_admin
), existing AS (
  SELECT c.user_id
  FROM candidates c
  JOIN byod_exam_participants p
    ON p.user_id = c.user_id AND p.exam_id = :'exam_id'
)
SELECT
  (SELECT count(*) FROM candidates),
  (SELECT count(*) FROM existing),
  (SELECT count(*) FROM candidates c WHERE NOT EXISTS (SELECT 1 FROM existing e WHERE e.user_id = c.user_id));
SQL
)"
IFS=$'\t' read -r candidate_count existing_count missing_count <<< "$counts"

echo "Exam: ${exam_name} (#${exam_hashtag}, state=${exam_state})"
echo "Enabled ordinary users: ${candidate_count}"
echo "Already in roster: ${existing_count}"
echo "Missing: ${missing_count}"

if [[ "$apply" != true ]]; then
  echo "Dry run; no changes made. Use --apply to insert missing students."
  exit 0
fi

[[ "$missing_count" =~ ^[0-9]+$ ]] || die "could not parse database counts"
if [[ "$missing_count" == 0 ]]; then
  echo "No missing students; nothing to do."
  exit 0
fi

actor_id="$(psql_exec -At -v issuer="$issuer" <<'SQL'
SELECT id
FROM byod_users
WHERE issuer = :'issuer' AND enabled AND platform_admin
ORDER BY created_at, id
LIMIT 1;
SQL
)"
[[ -n "$actor_id" ]] || die "no enabled platform admin exists for audit attribution"

psql_exec -v exam_id="$exam_id" -v issuer="$issuer" -v actor_id="$actor_id" <<'SQL'
BEGIN;
WITH exam AS (
  SELECT id AS exam_id
  FROM byod_exams
  WHERE id = :'exam_id'
  FOR UPDATE
), candidates AS (
  SELECT u.id AS user_id, e.exam_id
  FROM byod_users u
  CROSS JOIN exam e
  WHERE u.issuer = :'issuer'
    AND u.enabled
    AND NOT u.platform_admin
), added AS (
  INSERT INTO byod_exam_participants(exam_id, user_id, enabled)
  SELECT exam_id, user_id, true
  FROM candidates
  ON CONFLICT (exam_id, user_id) DO NOTHING
  RETURNING exam_id, user_id
), audited AS (
  INSERT INTO byod_user_audit(actor_id, user_id, action, details)
  SELECT :'actor_id', added.user_id, 'exam_access_bulk_granted',
         json_build_object(
           'exam_id', added.exam_id,
           'scope', 'enabled_non_platform_users',
           'source', 'add-all-students-to-exam.sh'
         )::text
  FROM added
  RETURNING user_id
)
SELECT
  (SELECT count(*) FROM added) AS added,
  (SELECT count(*) FROM audited) AS audit_events;
COMMIT;
SQL

echo "Done. Existing participant rows were not changed."
