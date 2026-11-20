# Publish to the Prusa Marketplace

The public repository is `NilsN3DP/prusa-obico-bridge`. Prusa derives the extension ID `com.github.aux.nilsn3dp.prusa-obico-bridge` from the module path. The workflow uses Prusa's verified builder pinned to a commit, with the SDK pinned to the Connect Local beta tag.

1. Generate an RSA signing key and register its public key in the [Prusa author console](https://plugin-marketplace.prusa3d.com/author/keys).

   ```sh
   openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out private_key.pem
   openssl pkey -in private_key.pem -pubout -out public_key.pem
   gh secret set PRUSA_CONNECT_EXTENSION_SIGNING_KEY \
     --repo NilsN3DP/prusa-obico-bridge --env release < private_key.pem
   ```

   Run the commands outside the repository and keep the private key in secure storage.

2. Register the trusted publisher in the [Connect author console](https://plugin-marketplace.prusa3d.com/author/connect). Use extension ID `com.github.aux.nilsn3dp.prusa-obico-bridge`, repository `NilsN3DP/prusa-obico-bridge`, workflow `marketplace.yml` and GitHub environment `release`.
3. Keep `module.yaml` and the release tag in sync. Run a dry build for the public tag. When the key and publisher are configured, run the same workflow with submission enabled.

   ```sh
   gh workflow run marketplace.yml --repo NilsN3DP/prusa-obico-bridge \
     --ref v0.1.7 -f dry-run=true
   gh workflow run marketplace.yml --repo NilsN3DP/prusa-obico-bridge \
     --ref v0.1.7 -f dry-run=false
   ```

The second run submits the release to Prusa's beta channel for review. Marketplace visibility follows approval by Prusa. [Official publishing documentation](https://github.com/prusa3d/prusa-connect-marketplace-publish)
