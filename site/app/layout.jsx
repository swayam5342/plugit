import { Instrument_Sans } from 'next/font/google';
import Script from 'next/script';
import './globals.css';

const instrument = Instrument_Sans({
  subsets: ['latin'],
  variable: '--font-instrument',
  display: 'swap',
});

const base = process.env.NEXT_PUBLIC_BASE_PATH || '';

export const metadata = {
  // Where the site is hosted, for absolute share-image URLs.
  metadataBase: new URL(process.env.NEXT_PUBLIC_SITE_URL || 'https://swayam5342.github.io'),
  title: 'PlugIt: plug any REST API into your AI',
  description:
    'PlugIt turns an OpenAPI, Swagger or Postman spec into curated, safe MCP tools for Claude, Codex, Cursor and VS Code. One local binary, no accounts, no telemetry.',
  openGraph: {
    title: 'PlugIt: plug any REST API into your AI',
    description: 'Turn any REST API into a curated, safe MCP server from your terminal.',
    images: [`${base}/media/still-picker.jpg`],
  },
  icons: {
    icon: "data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'%3E%3Crect width='32' height='32' fill='%23000000'/%3E%3Cpath d='M16 4l3 9 9 3-9 3-3 9-3-9-9-3 9-3z' fill='%234cb3f0'/%3E%3C/svg%3E",
  },
};

export const viewport = { themeColor: '#000000' };

export default function RootLayout({ children }) {
  return (
    // The js class is added before hydration, so React is told to expect it.
    <html lang="en" className={instrument.variable} suppressHydrationWarning>
      <body>
        {/* Entrance motion only applies once script runs: without it, everything is visible. */}
        <Script id="js-class" strategy="beforeInteractive">
          {"document.documentElement.classList.add('js')"}
        </Script>
        {children}
      </body>
    </html>
  );
}
