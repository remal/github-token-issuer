terraform {
  required_version = "~> 1.14"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "7.16.0"
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

# Service Account for Cloud Run
resource "google_service_account" "cloud_run_sa" {
  account_id   = "gh-repo-token-issuer-sa"
  display_name = "GitHub Repository Token Issuer Service Account"
  description  = "Service account for github-repository-token-issuer Cloud Run service"
}

# Grant Secret Manager access to service account
resource "google_secret_manager_secret_iam_member" "secret_accessor" {
  secret_id = "github-app-private-key"
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.cloud_run_sa.email}"
}

# Cloud Run service
resource "google_cloud_run_v2_service" "github_token_issuer" {
  name     = "github-repository-token-issuer"
  location = var.region

  template {
    service_account = google_service_account.cloud_run_sa.email

    scaling {
      min_instance_count = 0
      max_instance_count = 10
    }

    containers {
      # Placeholder image - actual deployment via gcloud run deploy --source
      image = "us-docker.pkg.dev/cloudrun/container/hello"

      resources {
        limits = {
          memory = "128Mi"
          cpu    = "0.5"
        }
      }

      env {
        name  = "GITHUB_APP_ID"
        value = var.github_app_id
      }
    }

    timeout = "60s"
  }

  # Deployments are managed by gcloud, not Terraform
  lifecycle {
    ignore_changes = [
      template[0].containers[0].image,
      template[0].revision,
      client,
      client_version,
    ]
  }
}

# IAM binding to allow GitHub OIDC tokens to invoke Cloud Run
# This allows any repository to invoke the service
# Authorization is handled by the service itself (checks if GitHub App is installed)
resource "google_cloud_run_v2_service_iam_member" "github_oidc_invoker" {
  project  = var.project_id
  location = google_cloud_run_v2_service.github_token_issuer.location
  name     = google_cloud_run_v2_service.github_token_issuer.name
  role     = "roles/run.invoker"
  member   = "principalSet://iam.googleapis.com/projects/${data.google_project.project.number}/locations/global/workloadIdentityPools/${google_iam_workload_identity_pool.github_actions.workload_identity_pool_id}/*"
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
  # Authorization is handled by the service itself
}

# Data source to get project number
data "google_project" "project" {
  project_id = var.project_id
}
