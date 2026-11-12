// Prefixes a public file with the base path (GitHub Pages project sites
// live under /<repo>/), for <img> and <video> tags next/image doesn't own.
export const asset = (p) => `${process.env.NEXT_PUBLIC_BASE_PATH || ''}${p}`;
