"""Local TLS fake Shopify for explicit OCI tests; no live merchant access."""
import json, ssl, sys, threading
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

certificate,key,port=sys.argv[1:]
lock=threading.RLock()
state={'product':None,'quantity':0,'active':False,'published':False,'creations':0,'cache':{},'fail_order':False,'fail_update':False,'drop_create':False,'unsafe_updates':0,'requests':0,'revision':1}
def money(s): return {'shopMoney':{'amount':s,'currencyCode':'EGP'}}
def variant():
 return {'id':'gid://shopify/ProductVariant/501','sku':state['product']['sku'],'inventoryItem':{'id':'gid://shopify/InventoryItem/502','tracked':True,'inventoryLevels':{'nodes':[{'updatedAt':(datetime(2026,1,1,tzinfo=timezone.utc)+timedelta(seconds=state['revision'])).isoformat(),'location':{'id':'gid://shopify/Location/10'},'quantities':[{'name':'available','quantity':state['quantity']}]}] if state['active'] else []}}}
def product():
 p=state['product']
 if not p: return None
 return {'id':'gid://shopify/Product/500','status':p['status'],'metafields':{'nodes':[{'namespace':'moonlight','key':k,'value':v} for k,v in p['fields'].items()]},'variants':{'nodes':[variant()]}}
def connection(nodes,after):
 start=int(after or 0);end=min(start+50,len(nodes))
 return {'nodes':nodes[start:end],'pageInfo':{'hasNextPage':end<len(nodes),'endCursor':str(end)}}
class Handler(BaseHTTPRequestHandler):
 def log_message(self,*args): pass
 def send(self,body,status=200):
  encoded=json.dumps(body).encode();self.send_response(status);self.send_header('X-Shopify-API-Version','2026-10');self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(encoded)));self.end_headers();self.wfile.write(encoded)
 def do_GET(self):
  with lock:self.send({k:v for k,v in state.items() if k!='cache'})
 def do_POST(self):
  body=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
  with lock:
   if self.path=='/control':state.update(body);state['revision']+=1;self.send({'ok':True});return
   if self.headers.get('X-Shopify-Access-Token')!='local-runtime-token':self.send({'errors':[{'message':'denied'}]},401);return
   state['requests']+=1
   q,v=body['query'],body.get('variables',{})
   op=next((name for name in ['ShopCurrency','ProductCreate','ProductUpdate','ManagedVariantUpdate','MetafieldsSet','Publish','Unpublish','InventoryActivate','InventorySet','VariantsBySKU','Order','InventoryVariant','Product'] if 'Moonlight'+name in q),None)
   if op=='ShopCurrency':data={'shop':{'currencyCode':'EGP'}}
   elif op=='ProductCreate':
    assert 'ProductSetInput!' in q
    p=v['input'];var=p['variants'][0];assert 'sku' in var
    state['product']={'sku':var['sku'],'title':p['title'],'desc':p['descriptionHtml'],'price':var['price'],'status':p['status'],'fields':{m['key']:m['value'] for m in p['metafields']}}
    state['creations']+=1;data={'productSet':{'product':{'id':'gid://shopify/Product/500','variants':{'nodes':[{'id':'gid://shopify/ProductVariant/501','sku':var['sku']}]}},'userErrors':[]}}
    if state['drop_create']:state['drop_create']=False;self.connection.shutdown(2);self.connection.close();return
   elif op=='Product':data={'product':product()}
   elif op=='InventoryVariant':data={'productVariant':variant()}
   elif op=='VariantsBySKU':
    nodes=[]
    if state['product']:
     nodes=[{'id':'gid://shopify/ProductVariant/'+str(10000+i),'sku':state['product']['sku']+'-FUZZY-'+str(i),'product':{'id':'gid://shopify/Product/'+str(20000+i)}} for i in range(60)]
     nodes.append({'id':'gid://shopify/ProductVariant/501','sku':state['product']['sku'],'product':product()})
    assert 'after: $after' in q and 'pageInfo' in q
    data={'productVariants':connection(nodes,v.get('after'))}
   elif op=='ProductUpdate':
    assert 'ProductInput!' in q
    if state['quantity']!=0:state['unsafe_updates']+=1
    if state['fail_update']:self.send({'errors':[{'message':'injected later stage failure'}]},503);return
    p=v['input'];state['product']['title']=p['title'];state['product']['desc']=p['descriptionHtml'];state['product']['status']=p.get('status',state['product']['status']);data={'productUpdate':{'product':{'id':'gid://shopify/Product/500'},'userErrors':[]}}
   elif op=='ManagedVariantUpdate':
    p=v['variants'][0];assert 'sku' not in p and 'sku' in p['inventoryItem']
    state['product']['sku']=p['inventoryItem']['sku'];state['product']['price']=p['price'];data={'productVariantsBulkUpdate':{'productVariants':[{'id':'gid://shopify/ProductVariant/501','sku':p['inventoryItem']['sku']}],'userErrors':[]}}
   elif op=='MetafieldsSet':
    assert '[MetafieldsSetInput!]!' in q
    for m in v['metafields']:state['product']['fields'][m['key']]=m['value']
    data={'metafieldsSet':{'metafields':[],'userErrors':[]}}
   elif op in ('Publish','Unpublish'):
    assert '[PublicationInput!]!' in q and 'input: $input' in q and '... on Product { id }' in q
    assert v['input']==[{'publicationId':'gid://shopify/Publication/11'}]
    state['published']=op=='Publish';field='publishablePublish' if op=='Publish' else 'publishableUnpublish';data={field:{'publishable':{'id':'gid://shopify/Product/500'},'userErrors':[]}}
   elif op=='InventoryActivate':
    k=v['idempotencyKey']
    if k in state['cache']:data=state['cache'][k]
    else:
     state['active']=True;state['quantity']=0;state['revision']+=1;data={'inventoryActivate':{'inventoryLevel':{'id':'gid://shopify/InventoryLevel/1'},'userErrors':[]}};state['cache'][k]=data
   elif op=='InventorySet':
    k=v['idempotencyKey']
    if k in state['cache']:data=state['cache'][k]
    else:
     p=v['input']['quantities'][0];assert p['inventoryItemId']=='gid://shopify/InventoryItem/502' and p['locationId']=='gid://shopify/Location/10'
     if state['quantity']!=p['changeFromQuantity']:data={'inventorySetQuantities':{'inventoryAdjustmentGroup':None,'userErrors':[{'code':'CHANGE_FROM_QUANTITY_STALE','message':'stale'}]}}
     else:state['quantity']=p['quantity'];state['revision']+=1;data={'inventorySetQuantities':{'inventoryAdjustmentGroup':{'id':'gid://shopify/InventoryAdjustmentGroup/1'},'userErrors':[]}}
     state['cache'][k]=data
   elif op=='Order':
    if state['fail_order']:self.send({'errors':[{'message':'injected fetch failure'}]},503);return
    oid=v['id'].split('/')[-1]
    assert 'after: $after' in q and 'pageInfo' in q
    lines=[{'id':'gid://shopify/LineItem/'+str(10000+i),'title':'local order line','sku':state['product']['sku'],'quantity':1,'originalTotalSet':money('1.00'),'discountedTotalSet':money('90071992547409.93' if i==50 else '1.00'),'taxLines':[],'product':{'id':'gid://shopify/Product/500'}} for i in range(51)]
    data={'order':{'id':v['id'],'legacyResourceId':oid,'name':'#local','createdAt':'2026-09-20T10:00:00Z','updatedAt':'2026-09-20T11:00:00Z','displayFinancialStatus':'PAID','displayFulfillmentStatus':'UNFULFILLED','currencyCode':'EGP','totalPriceSet':money('90071992547409.93'),'totalShippingPriceSet':money('0.00'),'totalTaxSet':money('0.00'),'totalDiscountsSet':money('0.00'),'lineItems':connection(lines,v.get('after'))}}
   else:self.send({'errors':[{'message':'unknown contract'}]});return
   self.send({'data':data})
class TestServer(ThreadingHTTPServer):
 def get_request(self):
  try:return super().get_request()
  except ssl.SSLError as e:print('TLS fixture handshake:',str(e),flush=True);raise
server=TestServer(('127.0.0.1',int(port)),Handler)
context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);context.load_cert_chain(certificate,key);server.socket=context.wrap_socket(server.socket,server_side=True)
print('READY',flush=True)
server.serve_forever()
