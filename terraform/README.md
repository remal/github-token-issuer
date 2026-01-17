# Terraform Infrastructure

This directory contains the Terraform configuration for deploying the GitHub Repository Token Issuer to Google Cloud Platform.

## Prerequisites

1. **GCP Project** with billing enabled
2. **Terraform** installed (>= 1.0)
3. **gcloud CLI** installed and authenticated
4. **GitHub App** created with repository permissions
5. **GitHub App private key** stored in GCP Secret Manager

## Required GCP APIs

Enable the following APIs in your GCP project:

```bash
gcloud services enable run.googleapis.com
gcloud services enable secretmanager.googleapis.com
gcloud services enable iamcredentials.googleapis.com
gcloud services enable artifactregistry.googleapis.com
```

## Setup Steps

### 1. Store GitHub App Private Key

Before running Terraform, create the Secret Manager secret with your GitHub App private key:

```bash
gcloud secrets create github-app-private-key \
  --data-file=path/to/your-private-key.pem
```

### 2. Configure Terraform Backend

Update the GCS bucket name in `main.tf`:

```hcl
backend "gcs" {
  bucket = "your-terraform-state-bucket"  # Change this to your bucket
  prefix = "github-repository-token-issuer"
}
```

Create the bucket if it doesn't exist:

```bash
gcloud storage buckets create gs://your-terraform-state-bucket \
  --location=us-east4 \
  --uniform-bucket-level-access
```

### 3. Configure Variables

Copy the example variables file and fill in your values:

```bash
cp terraform.tfvars.example terraform.tfvars
```

Edit `terraform.tfvars`:

```hcl
project_id    = "your-gcp-project-id"
region        = "us-east4"
github_app_id = "123456"  # Your GitHub App ID
```

### 4. Initialize Terraform

```bash
terraform init
```

### 5. Review the Plan

```bash
terraform plan
```

### 6. Apply Configuration

```bash
terraform apply
```

After successful deployment, Terraform will output the Cloud Run service URL.

## Resources Created

This configuration creates the following GCP resources:

- **Cloud Run Service** (`github-repository-token-issuer`) - The serverless function
- **Service Account** (`gh-repo-token-issuer-sa`) - Identity for Cloud Run
- **Artifact Registry Repository** - Docker container registry
- **Workload Identity Pool** (`github-actions`) - For GitHub OIDC authentication
- **Workload Identity Pool Provider** (`github-oidc`) - GitHub OIDC configuration
- **IAM Bindings** - Permissions for Cloud Run invocation and Secret Manager access

## Deploying Code Changes

When you make changes to the Go code in `function/`, you need to rebuild and redeploy the container:

### Option 1: Using Cloud Build

```bash
# Build and push the container
gcloud builds submit ../function \
  --tag us-east4-docker.pkg.dev/PROJECT_ID/github-repository-token-issuer/app:latest

# Terraform will detect the new image on next apply
terraform apply
```

### Option 2: Local Build and Push

```bash
# Build locally
cd ../function
docker build -t us-east4-docker.pkg.dev/PROJECT_ID/github-repository-token-issuer/app:latest .

# Push to Artifact Registry
docker push us-east4-docker.pkg.dev/PROJECT_ID/github-repository-token-issuer/app:latest

# Update Cloud Run service
cd ../terraform
terraform apply
```

## Outputs

After deployment, Terraform provides:

- `cloud_run_url` - The HTTPS endpoint for your service
- `service_account_email` - The service account email
- `artifact_registry_repository` - The container registry URL
- `workload_identity_pool_provider` - The WIF provider name

## Updating Configuration

To update environment variables or scaling settings, modify `main.tf` and run:

```bash
terraform apply
```

## Destroying Resources

To remove all created resources:

```bash
terraform destroy
```

**Warning**: This will delete the Cloud Run service and all associated resources. The Secret Manager secret and GCS state bucket are not deleted automatically.

## Troubleshooting

### Secret Manager Access Error

If Cloud Run can't access the secret:

```bash
gcloud secrets add-iam-policy-binding github-app-private-key \
  --member="serviceAccount:gh-repo-token-issuer-sa@PROJECT_ID.iam.gserviceaccount.com" \
  --role="roles/secretmanager.secretAccessor"
```

### Container Build Fails

Ensure you're in the correct directory and have the proper permissions:

```bash
gcloud auth configure-docker us-east4-docker.pkg.dev
```

### Workload Identity Federation Issues

Verify the WIF configuration:

```bash
gcloud iam workload-identity-pools describe github-actions \
  --location=global \
  --format=json
```

## Notes

- **No Logging**: This service intentionally has no logging enabled to reduce costs and complexity
- **Stateless**: No persistent storage; all state is managed per-request
- **Auto-scaling**: Configured for 0-10 instances with 80 concurrent requests per instance
- **Cost Optimization**: Minimum instances set to 0 to avoid idle charges
