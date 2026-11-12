// Static export: `npm run build` writes plain files to out/, which any static
// host serves. For a GitHub Pages project site (https://<user>.github.io/plugit/)
// build with NEXT_PUBLIC_BASE_PATH=/plugit.
const basePath = process.env.NEXT_PUBLIC_BASE_PATH || '';

/** @type {import('next').NextConfig} */
const nextConfig = {
  output: 'export',
  basePath,
  trailingSlash: true,
  images: { unoptimized: true },
  poweredByHeader: false,
};

export default nextConfig;
