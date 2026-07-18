"use client";

import React, { useState } from 'react';
import { CodeSnippet } from './CodeSnippet';
import { useTheme } from '@/context/ThemeContext';

type Platform = 'Kubernetes' | 'Docker' | 'Node.js' | 'Go' | null;

export function Wizard() {
  const { tokens: t } = useTheme();
  const [step, setStep] = useState(1);
  const [platform, setPlatform] = useState<Platform>(null);
  const [apiKey, setApiKey] = useState<string | null>(null);

  const platforms = [
    { name: 'Kubernetes', desc: 'Helm chart & DaemonSet', icon: 'deployed_code' },
    { name: 'Docker', desc: 'Container logs & metrics', icon: 'inventory_2' },
    { name: 'Node.js', desc: 'OTel JS Auto-instrumentation', icon: 'terminal' },
    { name: 'Go', desc: 'Native Go agent & Profiling', icon: 'memory' },
  ];

  const generateKey = () => {
    // Simulate API call
    setTimeout(() => {
      setApiKey(`pt_${Math.random().toString(36).substring(2, 15)}_${Math.random().toString(36).substring(2, 15)}`);
      setStep(3);
    }, 600);
  };

  const getScriptForPlatform = () => {
    if (!apiKey) return '';
    switch (platform) {
      case 'Kubernetes':
        return `helm repo add pulsetrace https://charts.pulsetrace.com\nhelm install pt-agent pulsetrace/agent \\\n  --set apiKey=${apiKey} \\\n  --namespace pulsetrace-system --create-namespace`;
      case 'Docker':
        return `docker run -d --name pulsetrace-agent \\\n  -e PT_API_KEY=${apiKey} \\\n  -v /var/run/docker.sock:/var/run/docker.sock \\\n  pulsetrace/agent:latest`;
      case 'Node.js':
        return `npm install @pulsetrace/agent\n\n// Add to top of your entry file (index.js):\nrequire('@pulsetrace/agent').init({ apiKey: '${apiKey}' });`;
      case 'Go':
        return `go get github.com/pulsetrace/agent-go\n\n// Add to main.go:\nimport "github.com/pulsetrace/agent-go"\n\nfunc main() {\n    agent.Init("${apiKey}")\n    defer agent.Flush()\n}`;
      default:
        return '';
    }
  };

  return (
    <div style={{ flex: 1, padding: '40px 28px', display: 'flex', justifyContent: 'center' }}>
      <div style={{
        width: '100%',
        maxWidth: '760px',
        padding: '44px',
        borderRadius: '26px',
        background: t.panelBg,
        border: '1px solid ' + t.panelBorder,
        backdropFilter: 'blur(30px) saturate(180%)',
        boxShadow: t.shadow,
        alignSelf: 'flex-start'
      }}>

        {/* Progress Indicator */}
        <div style={{ display: 'flex', gap: '10px', marginBottom: '36px' }}>
          {[1, 2, 3].map((s) => (
            <div key={s} style={{
              flex: 1,
              height: '4px',
              background: step >= s ? t.green : (t.dark ? 'rgba(255,255,255,0.1)' : 'rgba(0,0,0,0.08)'),
              borderRadius: '2px',
              transition: 'background 0.3s ease'
            }} />
          ))}
        </div>

        {step === 1 && (
          <div style={{ animation: 'fadeIn 0.5s ease' }}>
            <h2 style={{ fontSize: '28px', fontWeight: 700, margin: '0 0 8px' }}>Welcome to PulseTrace.</h2>
            <p style={{ color: t.text2, margin: '0 0 32px', fontSize: '14.5px', lineHeight: 1.6 }}>Let&apos;s get your telemetry flowing. Where is your application running?</p>

            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '16px' }}>
              {platforms.map(p => {
                const selected = platform === p.name;
                return (
                  <div
                    key={p.name}
                    onClick={() => setPlatform(p.name as Platform)}
                    style={{
                      padding: '24px',
                      borderRadius: '16px',
                      cursor: 'pointer',
                      border: selected ? '1.5px solid ' + t.green : '1px solid ' + t.panelBorder,
                      background: selected
                        ? (t.dark ? 'rgba(52,199,126,0.1)' : 'rgba(37,169,107,0.06)')
                        : (t.dark ? 'rgba(255,255,255,0.03)' : 'rgba(255,255,255,0.4)'),
                      transition: 'all 0.2s ease'
                    }}
                  >
                    <span className="material-symbols-outlined" style={{
                      fontSize: '30px',
                      color: selected ? t.green : t.accent,
                      marginBottom: '12px',
                      display: 'block'
                    }}>{p.icon}</span>
                    <h3 style={{ fontSize: '17px', margin: '0 0 4px', color: t.text1 }}>{p.name}</h3>
                    <p style={{ margin: 0, fontSize: '13px', color: t.text2 }}>{p.desc}</p>
                  </div>
                );
              })}
            </div>

            <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: '32px' }}>
              <button
                disabled={!platform}
                onClick={() => setStep(2)}
                style={{
                  background: platform ? t.text1 : (t.dark ? 'rgba(255,255,255,0.1)' : 'rgba(0,0,0,0.08)'),
                  color: platform ? (t.dark ? '#0b0c10' : '#fff') : t.text2,
                  padding: '13px 26px',
                  borderRadius: '10px',
                  border: 'none',
                  fontWeight: 600,
                  cursor: platform ? 'pointer' : 'not-allowed',
                  transition: 'all 0.2s'
                }}
              >
                Continue
              </button>
            </div>
          </div>
        )}

        {step === 2 && (
          <div style={{ animation: 'fadeIn 0.5s ease' }}>
            <h2 style={{ fontSize: '28px', fontWeight: 700, margin: '0 0 8px' }}>Generate Ingestion Key</h2>
            <p style={{ color: t.text2, margin: '0 0 32px', fontSize: '14.5px', lineHeight: 1.6 }}>
              This key will allow your {platform} environment to securely send traces, logs, and metrics to PulseTrace.
            </p>

            <div style={{
              background: t.dark ? 'rgba(0,0,0,0.2)' : 'rgba(0,0,0,0.03)',
              padding: '36px',
              borderRadius: '16px',
              textAlign: 'center',
              border: '1px solid ' + t.panelBorder
            }}>
              <button
                onClick={generateKey}
                style={{
                  background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`,
                  color: '#fff',
                  padding: '15px 30px',
                  borderRadius: '10px',
                  border: 'none',
                  fontWeight: 600,
                  fontSize: '15px',
                  cursor: 'pointer'
                }}
              >
                Generate API Key
              </button>
            </div>
          </div>
        )}

        {step === 3 && (
          <div style={{ animation: 'fadeIn 0.5s ease' }}>
            <h2 style={{ fontSize: '28px', fontWeight: 700, margin: '0 0 8px' }}>You&apos;re all set!</h2>
            <p style={{ color: t.text2, margin: '0 0 32px', fontSize: '14.5px', lineHeight: 1.6 }}>
              Run this command in your {platform} environment to start streaming telemetry.
            </p>

            <CodeSnippet code={getScriptForPlatform()} language={platform === 'Node.js' || platform === 'Go' ? 'javascript' : 'bash'} />

            <div style={{
              background: t.dark ? 'rgba(52,199,126,0.12)' : 'rgba(37,169,107,0.08)',
              color: t.green,
              padding: '16px',
              borderRadius: '12px',
              marginTop: '20px',
              display: 'flex',
              alignItems: 'center',
              gap: '12px',
              fontSize: '13.5px'
            }}>
              <span className="material-symbols-outlined" style={{ fontSize: '20px' }}>bolt</span>
              <div>
                <strong>Waiting for data...</strong>
                <div style={{ opacity: 0.8 }}>Once you run the command, data will appear on your dashboard within 3 seconds.</div>
              </div>
            </div>

            <div style={{ display: 'flex', justifyContent: 'center', marginTop: '40px' }}>
              <a href="/" style={{
                color: t.text1,
                textDecoration: 'none',
                background: t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.06)',
                padding: '11px 22px',
                borderRadius: '10px',
                fontSize: '13.5px',
                fontWeight: 600
              }}>
                Return to Dashboard
              </a>
            </div>
          </div>
        )}

      </div>
    </div>
  );
}
