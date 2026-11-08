import {services} from './service-fixtures.ts';
import type {components} from '../../src/api/generated.ts';
const definitions:[string,string,string,string[]][]=
[["web-storefront", "Web storefront", "Store experience", ["api-gateway", "feature-flags"]],
 ["mobile-backend", "Mobile backend", "Store experience", ["api-gateway", "push-delivery", "feature-flags"]],
 ["api-gateway", "API gateway", "Store experience", ["session", "authorization", "customer-profile", "product-catalog", "search", "recommendations", "cart", "checkout", "orders", "reviews", "loyalty", "subscriptions", "returns"]],
 ["merchant-portal", "Merchant portal", "Store experience", ["identity", "authorization", "product-catalog", "catalog-ingestion", "inventory", "reporting"]],
 ["identity", "Identity", "Identity & access", ["event-router", "audit-log", "configuration"]],
 ["session", "Session", "Identity & access", ["identity", "feature-flags"]],
 ["authorization", "Authorization", "Identity & access", ["identity", "configuration", "audit-log"]],
 ["customer-profile", "Customer profile", "Identity & access", ["identity", "event-router"]],
 ["address-book", "Address book", "Identity & access", ["customer-profile"]],
 ["product-catalog", "Product catalog", "Product discovery", ["product-media", "pricing", "configuration"]],
 ["catalog-ingestion", "Catalog ingestion", "Product discovery", ["product-catalog", "product-media", "event-router"]],
 ["product-media", "Product media", "Product discovery", ["configuration"]],
 ["search", "Search", "Product discovery", ["product-catalog", "pricing", "event-router"]],
 ["recommendations", "Recommendations", "Product discovery", ["analytics-ingestion", "product-catalog", "feature-flags"]],
 ["reviews", "Reviews", "Product discovery", ["identity", "product-catalog", "event-router"]],
 ["cart", "Cart", "Checkout & orders", ["product-catalog", "pricing", "promotions"]],
 ["checkout", "Checkout", "Checkout & orders", ["cart", "customer-profile", "address-book", "pricing", "promotions", "tax", "reservation", "payments", "orders", "feature-flags"]],
 ["orders", "Orders", "Checkout & orders", ["customer-profile", "event-router", "notifications"]],
 ["pricing", "Pricing", "Checkout & orders", ["feature-flags", "configuration"]],
 ["promotions", "Promotions", "Checkout & orders", ["loyalty", "feature-flags"]],
 ["tax", "Tax", "Checkout & orders", ["address-book", "configuration"]],
 ["subscriptions", "Subscriptions", "Checkout & orders", ["customer-profile", "payment-methods", "billing", "orders", "event-router"]],
 ["payments", "Payments", "Money movement", ["payment-methods", "payment-risk", "ledger", "event-router", "configuration"]],
 ["payment-methods", "Payment methods", "Money movement", ["identity", "configuration"]],
 ["payment-risk", "Payment risk", "Money movement", ["customer-profile", "analytics-ingestion", "feature-flags"]],
 ["billing", "Billing", "Money movement", ["payments", "ledger", "invoicing", "tax"]],
 ["invoicing", "Invoicing", "Money movement", ["customer-profile", "address-book", "tax", "ledger", "notifications"]],
 ["ledger", "Ledger", "Money movement", ["audit-log", "event-router"]],
 ["inventory", "Inventory", "Fulfillment & logistics", ["warehouse", "event-router"]],
 ["reservation", "Inventory reservation", "Fulfillment & logistics", ["inventory", "event-router"]],
 ["warehouse", "Warehouse", "Fulfillment & logistics", ["configuration"]],
 ["fulfillment", "Fulfillment", "Fulfillment & logistics", ["orders", "reservation", "warehouse", "shipping", "event-router"]],
 ["shipping", "Shipping", "Fulfillment & logistics", ["address-book", "configuration", "event-router"]],
 ["delivery-tracking", "Delivery tracking", "Fulfillment & logistics", ["shipping", "notifications", "event-router"]],
 ["returns", "Returns", "Fulfillment & logistics", ["orders", "inventory", "payments", "shipping", "event-router"]],
 ["notifications", "Notifications", "Customer engagement", ["email-delivery", "sms-delivery", "push-delivery", "customer-profile", "configuration"]],
 ["email-delivery", "Email delivery", "Customer engagement", ["configuration", "audit-log"]],
 ["sms-delivery", "SMS delivery", "Customer engagement", ["configuration", "audit-log"]],
 ["push-delivery", "Push delivery", "Customer engagement", ["configuration", "audit-log"]],
 ["customer-support", "Customer support", "Customer engagement", ["customer-profile", "orders", "returns", "delivery-tracking", "notifications"]],
 ["loyalty", "Loyalty", "Customer engagement", ["customer-profile", "ledger", "event-router"]],
 ["feature-flags", "Feature flags", "Platform engineering", ["configuration"]],
 ["configuration", "Configuration", "Platform engineering", []],
 ["event-router", "Event router", "Platform engineering", ["configuration", "audit-log"]],
 ["audit-log", "Audit log", "Platform engineering", []],
 ["analytics-ingestion", "Analytics ingestion", "Platform engineering", ["event-router"]],
 ["reporting", "Reporting", "Platform engineering", ["analytics-ingestion", "ledger", "orders"]]];
export function serviceFleet():components['schemas']['Service'][]{
 const rows:[string,string,string,string[]][]=[['unconnected-one','Unconnected one','',[]],['unconnected-two','Unconnected two','',[]],['unconnected-three','Unconnected three','',[]],...definitions];
 const ids=Object.fromEntries(rows.map(([name],i)=>[name,`00000000-0000-4000-8000-${String(i+1000).padStart(12,'0')}`]));
 const records:components['schemas']['Service'][]=rows.map(([name,display_name,owner,deps])=>({...services[9]!,id:ids[name]!,name,display_name,owner,description:`${display_name} in the retail demo.`,dependencies:deps.map(dep=>ids[dep]!),dependency_services:[],used_by:[],used_by_count:rows.filter(row=>row[3].includes(name)).length}));
 const ref=(record:components['schemas']['Service'])=>({id:record.id!,name:record.name,display_name:record.display_name,owner:record.owner??''});
 for(const record of records){record.dependency_services=(record.dependencies??[]).map(id=>ref(records.find(other=>other.id===id)!));record.used_by=records.filter(other=>(other.dependencies??[]).includes(record.id!)).map(ref);}
 return records;
}
