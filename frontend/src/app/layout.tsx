import type { Metadata } from "next";
import "./globals.css";
import { AppShell } from "@/components/Layout/AppShell";
import { AuthProvider } from "@/context/AuthContext";
import { ThemeProvider } from "@/context/ThemeContext";
import { ToastProvider } from "@/components/ui/Toast";

export const metadata: Metadata = {
  title: "PulseTrace - Observability AI",
  description: "Enterprise Grade Observability Platform",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en">
      <head>
        <link rel="preconnect" href="https://fonts.googleapis.com" />
        <link rel="preconnect" href="https://fonts.gstatic.com" crossOrigin="anonymous" />
        {/* eslint-disable-next-line @next/next/no-page-custom-font -- the rule's
            premise ("fonts not added in pages/_document.js load for a single
            page") is Pages Router specific: this is the App Router root layout,
            which wraps every route, so the font loads once for the whole app.
            Migrating to next/font/google would still be an improvement — it
            self-hosts and removes these two external requests — but Material
            Symbols is used as an icon font via the .material-symbols-outlined
            class and ligatures, so that conversion needs visual verification
            across every screen and belongs in its own change. */}
        <link
          href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700;800&family=Material+Symbols+Outlined:opsz,wght,FILL,GRAD@20,400,0,0&display=swap"
          rel="stylesheet"
        />
      </head>
      <body>
        <ThemeProvider>
          <AuthProvider>
            <ToastProvider>
              <AppShell>
                {children}
              </AppShell>
            </ToastProvider>
          </AuthProvider>
        </ThemeProvider>
      </body>
    </html>
  );
}
