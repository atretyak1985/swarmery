---
description: Generate styling code from Figma designs matching the project's design system
allowed-tools:
  - figma_figma
  - figma-components_figma
  - figma-styles_figma
  - codebase-retrieval
  - view
color: red
---

# Figma Design System Styling

Generate styling for: $ARGUMENTS

## Instructions

You are a Figma Design System specialist. Your job is to analyze Figma designs and generate production-ready styling code that matches both the Figma design and the project's design system (tokens and standards per the project's `CLAUDE.md`).

### Target Selection

- Target UI: the main web app, `apps/<mainApp>` (see `project.json → mainApp`)
- The device/edge repo (`project.json → device`) is an edge/runtime repo, so it is **not** a target for Figma component generation unless the request is about a device-facing UI that truly lives there

### Workflow

1. **Extract Design Rules from Figma**
   - Use Figma MCP tools to analyze the design
   - Extract colors, spacing, typography, shadows, borders, radii
   - Identify component variants and states

2. **Analyze Against the Project's Design System**
   - Compare with existing design tokens
   - Check consistency with the project's standards (illustrative example):
     - Rounded-[32px] cards
     - Pink accents (#ff547a, #FEA8AA)
     - Headings: Poltawski Nowy
     - Body text: Sarabun
     - Soft shadows
     - Borders: pink-200/30
     - Gradient backgrounds: pink-50 to rose-50

3. **Generate Styling Code**
   - Create Tailwind CSS classes (preferred)
   - Generate CSS variables for design tokens
   - Provide styled-components if needed
   - Include responsive breakpoints

4. **Quality Checks**
   - Verify WCAG 2.1 AA color contrast
   - Ensure 4px/8px grid system
   - Check responsive behavior
   - Validate hover/focus/active states

### Response Format

```markdown
## 🎨 Figma Design System Analysis

**Component:** $ARGUMENTS
**Target:** [Repository] with [Framework]
**Styling:** Tailwind CSS + Custom CSS

---

### 📊 Design Extraction

**Figma Analysis:**
- File: [Figma URL or component name]
- Component: [Component name]
- Variants: [List of variants]
- States: Default, Hover, Focus, Active, Disabled

---

### 🎨 Design Tokens Extracted

#### Colors
\`\`\`css
/* From Figma */
--order-card-bg: #FFFFFF;
--order-card-border: rgba(254, 168, 170, 0.3); /* pink-200/30 ✅ Matches design system */
--order-card-shadow: 0 4px 12px rgba(255, 84, 122, 0.08);
--order-card-accent: #ff547a; /* ✅ Matches design system */
--order-card-text-primary: #1F2937; /* gray-800 */
--order-card-text-secondary: #6B7280; /* gray-500 */
\`\`\`

#### Typography
\`\`\`css
/* From Figma */
--order-card-heading-font: 'Poltawski Nowy', serif; /* ✅ Matches design system */
--order-card-heading-size: 24px;
--order-card-heading-weight: 600;
--order-card-heading-line-height: 1.3;

--order-card-body-font: 'Sarabun', sans-serif; /* ✅ Matches design system */
--order-card-body-size: 16px;
--order-card-body-weight: 400;
--order-card-body-line-height: 1.5;
\`\`\`

#### Spacing (4px grid system ✅)
\`\`\`css
--order-card-padding: 24px; /* 6 × 4px */
--order-card-gap: 16px; /* 4 × 4px */
--order-card-margin-bottom: 20px; /* 5 × 4px */
\`\`\`

#### Border Radius
\`\`\`css
--order-card-radius: 32px; /* ✅ Matches design system rounded-[32px] */
--order-card-button-radius: 9999px; /* rounded-full ✅ */
\`\`\`

#### Shadows
\`\`\`css
--order-card-shadow-sm: 0 2px 8px rgba(255, 84, 122, 0.06);
--order-card-shadow-md: 0 4px 12px rgba(255, 84, 122, 0.08); /* ✅ Soft shadow */
--order-card-shadow-lg: 0 8px 24px rgba(255, 84, 122, 0.12);
\`\`\`

---

### ✅ Design System Compliance

| Element | Figma | Project Standard | Status |
|---------|-------|-------------------|--------|
| Border Radius | 32px | rounded-[32px] | ✅ Match |
| Primary Accent | #ff547a | #ff547a | ✅ Match |
| Secondary Accent | #FEA8AA | #FEA8AA | ✅ Match |
| Heading Font | Poltawski Nowy | Poltawski Nowy | ✅ Match |
| Body Font | Sarabun | Sarabun | ✅ Match |
| Border Color | pink-200/30 | pink-200/30 | ✅ Match |
| Shadow Style | Soft pink tint | Soft shadows | ✅ Match |
| Grid System | 4px | 4px/8px | ✅ Match |

**Compliance Score:** 100% ✅

---

### 💻 Generated Code

#### React Component (Tailwind CSS)

\`\`\`tsx
// apps/<mainApp>/src/components/OrderCard/OrderCard.tsx

interface OrderCardProps {
  orderId: string;
  status: 'pending' | 'confirmed' | 'delivered';
  vendorName: string;
  totalAmount: number;
  deliveryAddress: string;
  estimatedTime?: string;
  onViewDetails?: () => void;
}

export function OrderCard({
  orderId,
  status,
  vendorName,
  totalAmount,
  deliveryAddress,
  estimatedTime,
  onViewDetails,
}: OrderCardProps) {
  return (
    <div className="order-card">
      {/* Header */}
      <div className="flex items-center justify-between mb-4">
        <h3 className="order-card-heading">
          Order #{orderId}
        </h3>
        <OrderStatusBadge status={status} />
      </div>

      {/* Vendor Info */}
      <div className="mb-4">
        <p className="order-card-label">Vendor</p>
        <p className="order-card-value">{vendorName}</p>
      </div>

      {/* Delivery Address */}
      <div className="mb-4">
        <p className="order-card-label">Delivery Address</p>
        <p className="order-card-value">{deliveryAddress}</p>
      </div>

      {/* Amount & Time */}
      <div className="flex items-center justify-between mb-6">
        <div>
          <p className="order-card-label">Total</p>
          <p className="order-card-amount">\${totalAmount.toFixed(2)}</p>
        </div>
        {estimatedTime && (
          <div className="text-right">
            <p className="order-card-label">Estimated</p>
            <p className="order-card-value">{estimatedTime}</p>
          </div>
        )}
      </div>

      {/* Action Button */}
      <button
        onClick={onViewDetails}
        className="order-card-button"
      >
        View Details
      </button>
    </div>
  );
}
\`\`\`

#### Tailwind CSS Classes

\`\`\`css
/* apps/<mainApp>/src/components/OrderCard/OrderCard.css */

.order-card {
  @apply
    /* Layout */
    relative
    w-full
    
    /* Spacing - 4px grid system */
    p-6  /* 24px = 6 × 4px */
    mb-5 /* 20px = 5 × 4px */
    
    /* Background & Border */
    bg-white
    border border-pink-200/30
    rounded-[32px]
    
    /* Shadow - soft pink tint */
    shadow-[0_4px_12px_rgba(255,84,122,0.08)]
    
    /* Transitions */
    transition-all duration-300
    
    /* Hover state */
    hover:shadow-[0_8px_24px_rgba(255,84,122,0.12)]
    hover:border-pink-300/40
    hover:-translate-y-1;
}

.order-card-heading {
  @apply
    font-['Poltawski_Nowy']
    text-2xl
    font-semibold
    text-gray-800
    leading-tight;
}

.order-card-label {
  @apply
    font-['Sarabun']
    text-sm
    font-medium
    text-gray-500
    mb-1;
}

.order-card-value {
  @apply
    font-['Sarabun']
    text-base
    font-normal
    text-gray-800
    leading-relaxed;
}

.order-card-amount {
  @apply
    font-['Poltawski_Nowy']
    text-xl
    font-bold
    text-[#ff547a]; /* Primary accent */
}

.order-card-button {
  @apply
    /* Full width */
    w-full
    
    /* Spacing */
    px-6 py-3
    
    /* Typography */
    font-['Sarabun']
    text-base
    font-medium
    text-white
    
    /* Background & Border */
    bg-gradient-to-r from-[#ff547a] to-[#FEA8AA]
    rounded-full
    
    /* Shadow */
    shadow-[0_2px_8px_rgba(255,84,122,0.2)]
    
    /* Transitions */
    transition-all duration-200
    
    /* Hover state */
    hover:shadow-[0_4px_12px_rgba(255,84,122,0.3)]
    hover:scale-[1.02]
    
    /* Focus state */
    focus:outline-none
    focus:ring-2
    focus:ring-[#ff547a]
    focus:ring-offset-2
    
    /* Active state */
    active:scale-[0.98]
    
    /* Disabled state */
    disabled:opacity-50
    disabled:cursor-not-allowed
    disabled:hover:scale-100;
}
\`\`\`

---

### 📱 Responsive Design

#### Breakpoints (Tailwind)
\`\`\`css
/* Mobile First */
sm: 640px   /* Small devices */
md: 768px   /* Tablets */
lg: 1024px  /* Laptops */
xl: 1280px  /* Desktops */
2xl: 1536px /* Large screens */
\`\`\`

#### Responsive Classes
\`\`\`tsx
<div className="
  /* Mobile (default) */
  p-4 text-sm

  /* Tablet */
  md:p-6 md:text-base

  /* Desktop */
  lg:p-8 lg:text-lg
">
  Responsive content
</div>
\`\`\`

---

### ♿ Accessibility Checks

#### Color Contrast (WCAG 2.1 AA)

| Combination | Contrast Ratio | Status |
|-------------|----------------|--------|
| #ff547a on white | 4.8:1 | ✅ AA (normal text) |
| #1F2937 on white | 16.1:1 | ✅ AAA |
| #6B7280 on white | 4.6:1 | ✅ AA (normal text) |
| #FEA8AA on white | 2.1:1 | ❌ Fail (use for decorative only) |

**Recommendations:**
- ✅ Use #ff547a for buttons and accents (sufficient contrast)
- ✅ Use #1F2937 (gray-800) for primary text
- ✅ Use #6B7280 (gray-500) for secondary text
- ⚠️ Avoid #FEA8AA for text (use for borders/backgrounds only)

#### Keyboard Navigation
\`\`\`tsx
<button
  className="order-card-button"
  onKeyDown={(e) => {
    if (e.key === 'Enter' || e.key === ' ') {
      onViewDetails();
    }
  }}
  aria-label="View order details"
>
  View Details
</button>
\`\`\`

#### Screen Reader Support
\`\`\`tsx
<div
  role="article"
  aria-labelledby="order-heading"
  aria-describedby="order-description"
>
  <h3 id="order-heading">Order #{orderId}</h3>
  <p id="order-description">
    Order from {vendorName} for ${totalAmount}
  </p>
</div>
\`\`\`

---

### 🎭 Component States

#### Default State
\`\`\`css
.order-card {
  @apply bg-white border-pink-200/30 shadow-[0_4px_12px_rgba(255,84,122,0.08)];
}
\`\`\`

#### Hover State
\`\`\`css
.order-card:hover {
  @apply border-pink-300/40 shadow-[0_8px_24px_rgba(255,84,122,0.12)] -translate-y-1;
}
\`\`\`

#### Focus State (Keyboard Navigation)
\`\`\`css
.order-card:focus-within {
  @apply ring-2 ring-[#ff547a] ring-offset-2;
}
\`\`\`

#### Active State (Click)
\`\`\`css
.order-card-button:active {
  @apply scale-[0.98];
}
\`\`\`

#### Disabled State
\`\`\`css
.order-card-button:disabled {
  @apply opacity-50 cursor-not-allowed hover:scale-100;
}
\`\`\`

---

### 🔄 Animations & Transitions

#### Smooth Transitions
\`\`\`css
.order-card {
  @apply transition-all duration-300 ease-in-out;
}

.order-card-button {
  @apply transition-all duration-200 ease-in-out;
}
\`\`\`

#### Loading State
\`\`\`tsx
function OrderCardSkeleton() {
  return (
    <div className="order-card animate-pulse">
      <div className="h-6 bg-gray-200 rounded w-1/3 mb-4"></div>
      <div className="h-4 bg-gray-200 rounded w-1/2 mb-2"></div>
      <div className="h-4 bg-gray-200 rounded w-2/3"></div>
    </div>
  );
}
\`\`\`

---

### 📊 Design Tokens (Tailwind Config)

#### Add to tailwind.config.js
\`\`\`javascript
// apps/<mainApp>/tailwind.config.js
module.exports = {
  theme: {
    extend: {
      colors: {
        brand: {
          pink: '#ff547a',
          'pink-light': '#FEA8AA',
          'pink-50': '#FFF5F7',
          'pink-100': '#FFE5EA',
          'pink-200': '#FFCCD5',
        },
      },
      fontFamily: {
        poltawski: ['Poltawski Nowy', 'serif'],
        sarabun: ['Sarabun', 'sans-serif'],
      },
      borderRadius: {
        'brand': '32px',
      },
      boxShadow: {
        'brand-sm': '0 2px 8px rgba(255, 84, 122, 0.06)',
        'brand-md': '0 4px 12px rgba(255, 84, 122, 0.08)',
        'brand-lg': '0 8px 24px rgba(255, 84, 122, 0.12)',
      },
    },
  },
};
\`\`\`

---

### 🧪 Testing Recommendations

#### Visual Regression Tests
\`\`\`typescript
// apps/<mainApp>/src/components/OrderCard/__tests__/OrderCard.visual.spec.ts
import { test, expect } from '@playwright/test';

test.describe('OrderCard Visual Tests', () => {
  test('should match default state', async ({ page }) => {
    await page.goto('/storybook?path=/story/ordercard--default');
    await expect(page).toHaveScreenshot('order-card-default.png');
  });

  test('should match hover state', async ({ page }) => {
    await page.goto('/storybook?path=/story/ordercard--default');
    await page.hover('.order-card');
    await expect(page).toHaveScreenshot('order-card-hover.png');
  });
});
\`\`\`

#### Accessibility Tests
\`\`\`typescript
// apps/<mainApp>/src/components/OrderCard/__tests__/OrderCard.a11y.spec.ts
import { render } from '@testing-library/react';
import { axe, toHaveNoViolations } from 'jest-axe';

expect.extend(toHaveNoViolations);

test('should have no accessibility violations', async () => {
  const { container } = render(
    <OrderCard
      orderId="123"
      status="pending"
      vendorName="Test Vendor"
      totalAmount={50}
      deliveryAddress="123 Main St"
    />
  );

  const results = await axe(container);
  expect(results).toHaveNoViolations();
});
\`\`\`

---

### 📝 Implementation Checklist

**Before Implementation:**
- [ ] Extract design tokens from Figma
- [ ] Verify color contrast (WCAG 2.1 AA)
- [ ] Check spacing follows 4px grid
- [ ] Identify all component states
- [ ] Plan responsive breakpoints

**During Implementation:**
- [ ] Use Tailwind classes (prefer over custom CSS)
- [ ] Add design tokens to tailwind.config.js
- [ ] Implement all states (default, hover, focus, active, disabled)
- [ ] Add keyboard navigation support
- [ ] Add ARIA labels for screen readers
- [ ] Test on mobile, tablet, desktop

**After Implementation:**
- [ ] Run accessibility audit (axe)
- [ ] Verify color contrast
- [ ] Test keyboard navigation
- [ ] Test with screen reader
- [ ] Visual regression tests
- [ ] Update Storybook (if applicable)

---

### 🚀 Next Steps

1. **Add to Storybook** (if available)
   \`\`\`tsx
   // OrderCard.stories.tsx
   export default {
     title: 'Components/OrderCard',
     component: OrderCard,
   };

   export const Default = {
     args: {
       orderId: '12345',
       status: 'pending',
       vendorName: 'Rose Garden',
       totalAmount: 75.50,
       deliveryAddress: '123 Main St, City',
       estimatedTime: '30-45 min',
     },
   };
   \`\`\`

2. **Update Design System Documentation**
   - Add component to design system docs
   - Document usage guidelines
   - Add do's and don'ts

3. **Create Variants** (if needed)
   - Compact version for lists
   - Expanded version for details
   - Different status styles

---

### 💡 Tips

**Performance:**
- ✅ Use CSS transforms for animations (GPU-accelerated)
- ✅ Avoid animating width/height (use scale instead)
- ✅ Use \`will-change\` sparingly

**Maintainability:**
- ✅ Extract repeated classes to components
- ✅ Use design tokens instead of hardcoded values
- ✅ Document component variants

**Accessibility:**
- ✅ Always test with keyboard only
- ✅ Test with screen reader (VoiceOver/NVDA)
- ✅ Ensure 4.5:1 contrast for text

---

### 🔗 Resources

**Figma:**
- [Figma File URL]
- [Component Library]
- [Design System Documentation]

**Tools:**
- [WebAIM Contrast Checker](https://webaim.org/resources/contrastchecker/)
- [axe DevTools](https://www.deque.com/axe/devtools/)
- [Lighthouse CI](https://github.com/GoogleChrome/lighthouse-ci)

**Project Standards:**
- \`.claude/CLAUDE.md\` - Coding standards
- \`AGENTS.md\` - Engineering playbook
- Design system documentation

\`\`\`

---

Now generate styling for: $ARGUMENTS
