"use client";

import React from 'react';
import { Wizard } from '@/components/Onboarding/Wizard';

export default function OnboardingPage() {
  return (
    <div style={{ 
      display: 'flex', 
      flexDirection: 'column', 
      minHeight: '80vh', 
      justifyContent: 'center',
      padding: '40px' 
    }}>
      <Wizard />
    </div>
  );
}
