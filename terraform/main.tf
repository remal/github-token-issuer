terraform {
  required_version = "~> 1.13"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "7.15.0"
    }
  }

  backend "gcs" {
    bucket = "github-repository-token-issuer-terraform-state"
    prefix = "github-repository-token-issuer"
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

# Service Account for Cloud Function
resource "google_service_account" "cloud_function_sa" {
  account_id   = "github-repository-token-issuer-sa"
  display_name = "GitHub Repository Token Issuer Service Account"
  description  = "Service account for github-repository-token-issuer Cloud Function"
}

# Grant Secret Manager access to service account
resource "google_secret_manager_secret_iam_member" "secret_accessor" {
  secret_id = "github-app-private-key"
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.cloud_function_sa.email}"
}

# Storage bucket for Cloud Function source code
resource "google_storage_bucket" "function_source" {
  name                        = "${var.project_id}-github-token-issuer-source"
  location                    = var.region
  uniform_bucket_level_access = true
  force_destroy               = true
}

# Cloud Function (2nd generation)
resource "google_cloudfunctions2_function" "github_token_issuer" {
  name     = "github-repository-token-issuer"
  location = var.region

  build_config {
    runtime     = "go125"
    entry_point = "TokenHandler"
    source {
      storage_source {
        bucket = google_storage_bucket.function_source.name
        object = google_storage_bucket_object.function_source_archive.name
      }
    }
  }

  service_config {
    max_instance_count    = 10
    min_instance_count    = 0
    available_memory      = "512Mi"
    timeout_seconds       = 60
    service_account_email = google_service_account.cloud_function_sa.email

    environment_variables = {
      GITHUB_APP_ID = var.github_app_id
    }
  }
}

# Archive function source code for deployment
data "archive_file" "function_source" {
  type        = "zip"
  source_dir  = "${path.module}/../function"
  output_path = "${path.module}/function-source.zip"
  excludes = [
    "go.sum",
  ]
}

# Upload function source to GCS
resource "google_storage_bucket_object" "function_source_archive" {
  name   = "function-source-${data.archive_file.function_source.output_md5}.zip"
  bucket = google_storage_bucket.function_source.name
  source = data.archive_file.function_source.output_path
}

# IAM binding to allow GitHub OIDC tokens to invoke Cloud Function
# This allows any repository to invoke the Cloud Function
# Authorization is handled by the function itself (checks if GitHub App is installed)
resource "google_cloudfunctions2_function_iam_member" "github_oidc_invoker" {
  project        = var.project_id
  location       = google_cloudfunctions2_function.github_token_issuer.location
  cloud_function = google_cloudfunctions2_function.github_token_issuer.name
  role           = "roles/cloudfunctions.invoker"
  member         = "principalSet://iam.googleapis.com/projects/${data.google_project.project.number}/locations/global/workloadIdentityPools/${google_iam_workload_identity_pool.github_actions.workload_identity_pool_id}/*"
}

# Workload Identity Pool for GitHub Actions
resource "google_iam_workload_identity_pool" "github_actions" {
  workload_identity_pool_id = "github-actions"
  display_name              = "GitHub Actions"
  description               = "Workload Identity Pool for GitHub Actions OIDC"
}

# Workload Identity Pool Provider for GitHub
resource "google_iam_workload_identity_pool_provider" "github" {
  workload_identity_pool_id          = google_iam_workload_identity_pool.github_actions.workload_identity_pool_id
  workload_identity_pool_provider_id = "github-oidc"
  display_name                       = "GitHub OIDC Provider"
  description                        = "OIDC provider for GitHub Actions"

  attribute_mapping = {
    "google.subject"       = "assertion.sub"
    "attribute.actor"      = "assertion.actor"
    "attribute.repository" = "assertion.repository"
    "attribute.aud"        = "assertion.aud"
  }

  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }

  # No attribute condition - allow any GitHub repository to authenticate
  # Authorization is handled by the function itself
}

# Data source to get project number
data "google_project" "project" {
  project_id = var.project_id
}
